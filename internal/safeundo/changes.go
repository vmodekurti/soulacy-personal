package safeundo

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"reflect"
	"slices"
	"strings"
	"unicode/utf8"
)

// Reject duplicate keys at every depth; different JSON decoders must not
// disagree about what an operator approved. Keep numbers lossless.
func parseJSON(raw []byte) (any, error) {
	return parseBoundedJSON(raw, MaxBody)
}

// ValidateRequest rejects duplicate keys, invalid UTF-8, excess depth and
// trailing values before a typed API decoder can accept an ambiguous request.
func ValidateRequest(raw []byte) error {
	_, err := parseBoundedJSON(raw, MaxJobBytes)
	return err
}

// DecodeRequest also enforces exact struct field names. encoding/json alone
// accepts case-insensitive aliases (e.g. confirmed + Confirmed), which can
// overwrite a decision even with DisallowUnknownFields enabled.
func DecodeRequest(raw []byte, target any) error {
	v, err := parseBoundedJSON(raw, MaxJobBytes)
	t := reflect.TypeOf(target)
	if err != nil || t == nil || t.Kind() != reflect.Pointer || reflect.ValueOf(target).IsNil() || !exactKeys(v, t) {
		return ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		return ErrInvalid
	}
	return nil
}

func exactKeys(v any, t reflect.Type) bool {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == reflect.TypeOf(json.RawMessage{}) || v == nil {
		return true
	}
	switch t.Kind() {
	case reflect.Struct:
		m, ok := v.(map[string]any)
		if !ok {
			return false
		}
		fields := map[string]reflect.Type{}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			tag := strings.Split(f.Tag.Get("json"), ",")[0]
			if f.IsExported() && tag != "-" {
				if tag == "" {
					tag = f.Name
				}
				fields[tag] = f.Type
			}
		}
		for k, value := range m {
			field, exists := fields[k]
			if !exists || !exactKeys(value, field) {
				return false
			}
		}
	case reflect.Slice:
		a, ok := v.([]any)
		if !ok {
			return false
		}
		for _, value := range a {
			if !exactKeys(value, t.Elem()) {
				return false
			}
		}
	case reflect.Map:
		m, ok := v.(map[string]any)
		if !ok {
			return false
		}
		for _, value := range m {
			if !exactKeys(value, t.Elem()) {
				return false
			}
		}
	}
	return true
}

func parseBoundedJSON(raw []byte, limit int) (any, error) {
	if len(raw) > limit || !utf8.Valid(raw) {
		return nil, ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var value func(int) (any, error)
	value = func(depth int) (any, error) {
		if depth > 32 {
			return nil, ErrInvalid
		}
		t, err := d.Token()
		if err != nil {
			return nil, ErrInvalid
		}
		delim, ok := t.(json.Delim)
		if !ok {
			return t, nil
		}
		switch delim {
		case '{':
			m := map[string]any{}
			for d.More() {
				key, err := d.Token()
				if err != nil {
					return nil, ErrInvalid
				}
				k, ok := key.(string)
				if !ok {
					return nil, ErrInvalid
				}
				if _, exists := m[k]; exists {
					return nil, ErrInvalid
				}
				v, err := value(depth + 1)
				if err != nil {
					return nil, err
				}
				m[k] = v
			}
			end, err := d.Token()
			if err != nil || end != json.Delim('}') {
				return nil, ErrInvalid
			}
			return m, nil
		case '[':
			a := []any{}
			for d.More() {
				v, err := value(depth + 1)
				if err != nil {
					return nil, err
				}
				a = append(a, v)
			}
			end, err := d.Token()
			if err != nil || end != json.Delim(']') {
				return nil, ErrInvalid
			}
			return a, nil
		default:
			return nil, ErrInvalid
		}
	}
	v, err := value(0)
	if err != nil {
		return nil, err
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, ErrInvalid
	}
	return v, nil
}

func fieldValue(m map[string]any, key string) Value {
	v, exists := m[key]
	if !exists {
		return Value{}
	}
	b, _ := json.Marshal(v)
	return Value{Exists: true, JSON: b}
}

func equalValue(a, b Value) bool {
	if a.Exists != b.Exists {
		return false
	}
	if !a.Exists || bytes.Equal(a.JSON, b.JSON) {
		return true
	}
	x, err := parseJSON(a.JSON)
	if err != nil {
		return false
	}
	y, err := parseJSON(b.JSON)
	if err != nil {
		return false
	}
	return equalJSON(x, y)
}

func equalJSON(a, b any) bool {
	switch x := a.(type) {
	case json.Number:
		y, ok := b.(json.Number)
		return ok && normalizedNumber(x) == normalizedNumber(y)
	case map[string]any:
		y, ok := b.(map[string]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for k, v := range x {
			other, exists := y[k]
			if !exists || !equalJSON(v, other) {
				return false
			}
		}
		return true
	case []any:
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for i := range x {
			if !equalJSON(x[i], y[i]) {
				return false
			}
		}
		return true
	default:
		return reflect.DeepEqual(a, b)
	}
}

// Compare JSON numbers mathematically, not as float64 or lexical strings:
// servers may serialize 1.0 as 1. Only the exponent is a big integer; never
// allocate 10^exponent, even for a hostile number such as 1e999999999999.
func normalizedNumber(n json.Number) string {
	s := n.String()
	negative := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	exponent := new(big.Int)
	if i := strings.IndexAny(s, "eE"); i >= 0 {
		exponent.SetString(s[i+1:], 10)
		s = s[:i]
	}
	fraction := 0
	if i := strings.IndexByte(s, '.'); i >= 0 {
		fraction = len(s) - i - 1
		s = s[:i] + s[i+1:]
	}
	s = strings.TrimLeft(s, "0")
	if s == "" {
		return "0"
	}
	coefficient := strings.TrimRight(s, "0")
	exponent.Add(exponent, big.NewInt(int64(len(s)-len(coefficient)-fraction)))
	if negative {
		coefficient = "-" + coefficient
	}
	return coefficient + "e" + exponent.String()
}

func makeAction(r Resource, input ChangeInput, snap snapshot) (Action, error) {
	a := Action{ResourceID: r.ID, Name: r.Name, Kind: r.Kind, Status: "pending", Fields: []FieldChange{}, Binding: binding(r), Version: snap.version}
	if r.Kind == "webdav_text" {
		a.Media = snap.media
		if input.Text == nil || len(input.Fields) != 0 || len(input.Remove) != 0 || len(*input.Text) > MaxBody || !utf8.ValidString(*input.Text) || strings.ContainsRune(*input.Text, 0) {
			return a, ErrInvalid
		}
		before, after := string(snap.body), *input.Text
		if before == after {
			return a, fmt.Errorf("%w: change has no effect", ErrInvalid)
		}
		a.BeforeText, a.AfterText = &before, &after
		return a, nil
	}
	if input.Text != nil || len(input.Fields)+len(input.Remove) == 0 || len(input.Fields)+len(input.Remove) > MaxFields {
		return a, ErrInvalid
	}
	keys := make([]string, 0, len(input.Fields)+len(input.Remove))
	for k := range input.Fields {
		keys = append(keys, k)
	}
	keys = append(keys, input.Remove...)
	slices.Sort(keys)
	for i, key := range keys {
		if !slices.Contains(r.Fields, key) || (i > 0 && keys[i-1] == key) {
			return a, ErrInvalid
		}
		before, after := fieldValue(snap.object, key), Value{}
		if raw, ok := input.Fields[key]; ok {
			value, err := parseJSON(raw)
			if err != nil {
				return a, err
			}
			canonical, _ := json.Marshal(value)
			if len(canonical) > MaxBody {
				return a, ErrLimit
			}
			after = Value{Exists: true, JSON: canonical}
		}
		if !equalValue(before, after) {
			a.Fields = append(a.Fields, FieldChange{Name: key, Before: before, After: after})
		}
	}
	if len(a.Fields) == 0 {
		return a, fmt.Errorf("%w: change has no effect", ErrInvalid)
	}
	if !resultFits(snap, a, true) {
		return a, ErrLimit
	}
	return a, nil
}

// A teammate may enlarge unrelated fields between prepare and preview. Check
// the projected full object again, so the resulting receipt remains readable.
func resultFits(snap snapshot, a Action, after bool) bool {
	if a.Kind == "webdav_text" {
		return true
	}
	result := make(map[string]any, len(snap.object))
	for k, v := range snap.object {
		result[k] = v
	}
	for _, f := range a.Fields {
		v := f.Before
		if after {
			v = f.After
		}
		if v.Exists {
			decoded, err := parseJSON(v.JSON)
			if err != nil {
				return false
			}
			result[f.Name] = decoded
		} else {
			delete(result, f.Name)
		}
	}
	encoded, err := json.Marshal(result)
	return err == nil && len(encoded) <= MaxBody
}

func matches(snap snapshot, a Action, after bool) bool {
	if a.Kind == "webdav_text" {
		v := a.BeforeText
		if after {
			v = a.AfterText
		}
		return v != nil && string(snap.body) == *v && snap.media == a.Media
	}
	for _, field := range a.Fields {
		want := field.Before
		if after {
			want = field.After
		}
		if !equalValue(fieldValue(snap.object, field.Name), want) {
			return false
		}
	}
	return true
}

func patchFor(a Action, direction string) []byte {
	type op struct {
		Op    string          `json:"op"`
		Path  string          `json:"path"`
		Value json.RawMessage `json:"value,omitempty"`
	}
	ops := []op{}
	for _, field := range a.Fields {
		from, to := field.Before, field.After
		if direction == "undo" {
			from, to = to, from
		}
		path := "/" + strings.ReplaceAll(strings.ReplaceAll(field.Name, "~", "~0"), "/", "~1")
		if from.Exists {
			ops = append(ops, op{Op: "test", Path: path, Value: from.JSON})
		}
		kind := "replace"
		if !to.Exists {
			kind = "remove"
		} else if !from.Exists {
			kind = "add"
		}
		ops = append(ops, op{Op: kind, Path: path, Value: to.JSON})
	}
	b, _ := json.Marshal(ops)
	return b
}
