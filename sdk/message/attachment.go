// attachment.go — typed media attachment parts for multi-modal messages.
// Defines the TypedPart interface and concrete types for text, file, image,
// audio, and location payloads.  A discriminated JSON deserializer
// (UnmarshalPartJSON) reads the "kind" field and dispatches to the correct
// concrete type.
package message

import (
	"encoding/json"
	"fmt"
)

// TypedPart is a typed attachment that can be embedded in a message alongside
// the legacy Part slice.  Concrete types are: TextPart, FilePart, ImagePart,
// AudioPart, LocationPart.
type TypedPart interface {
	// Kind returns the discriminant string used in JSON serialisation.
	// Valid values: "text", "file", "image", "audio", "location".
	Kind() string

	MarshalJSON() ([]byte, error)
}

// --- concrete types ---------------------------------------------------------

// TextPart carries a plain-text segment.
type TextPart struct {
	Text string `json:"text"`
}

func (t TextPart) Kind() string { return "text" }
func (t TextPart) MarshalJSON() ([]byte, error) {
	type wire TextPart
	return json.Marshal(struct {
		Kind string `json:"kind"`
		wire
	}{Kind: t.Kind(), wire: wire(t)})
}

// FilePart references a stored binary resource by ID.
type FilePart struct {
	ID       string `json:"id"`
	MIMEType string `json:"mime_type"`
	Filename string `json:"filename"`
}

func (f FilePart) Kind() string { return "file" }
func (f FilePart) MarshalJSON() ([]byte, error) {
	type wire FilePart
	return json.Marshal(struct {
		Kind string `json:"kind"`
		wire
	}{Kind: f.Kind(), wire: wire(f)})
}

// ImagePart references an image resource by ID, with optional dimensions.
type ImagePart struct {
	ID       string `json:"id"`
	MIMEType string `json:"mime_type"`
	Width    int    `json:"width,omitempty"`
	Height   int    `json:"height,omitempty"`
}

func (i ImagePart) Kind() string { return "image" }
func (i ImagePart) MarshalJSON() ([]byte, error) {
	type wire ImagePart
	return json.Marshal(struct {
		Kind string `json:"kind"`
		wire
	}{Kind: i.Kind(), wire: wire(i)})
}

// AudioPart references an audio resource by ID with an optional duration hint.
type AudioPart struct {
	ID          string `json:"id"`
	MIMEType    string `json:"mime_type"`
	DurationSec int    `json:"duration_sec,omitempty"`
}

func (a AudioPart) Kind() string { return "audio" }
func (a AudioPart) MarshalJSON() ([]byte, error) {
	type wire AudioPart
	return json.Marshal(struct {
		Kind string `json:"kind"`
		wire
	}{Kind: a.Kind(), wire: wire(a)})
}

// LocationPart carries a geographic coordinate with an optional label.
type LocationPart struct {
	Lat   float64 `json:"lat"`
	Lon   float64 `json:"lon"`
	Label string  `json:"label,omitempty"`
}

func (l LocationPart) Kind() string { return "location" }
func (l LocationPart) MarshalJSON() ([]byte, error) {
	type wire LocationPart
	return json.Marshal(struct {
		Kind string `json:"kind"`
		wire
	}{Kind: l.Kind(), wire: wire(l)})
}

// --- discriminated deserialiser ---------------------------------------------

// kindProbe is used solely to read the "kind" field before dispatching to
// the correct concrete unmarshaller.
type kindProbe struct {
	Kind string `json:"kind"`
}

// UnmarshalPartJSON decodes a JSON object that contains a "kind" field and
// returns the appropriate TypedPart implementation.  Returns an error if the
// "kind" is missing or unrecognised.
func UnmarshalPartJSON(data []byte) (TypedPart, error) {
	var probe kindProbe
	if err := json.Unmarshal(data, &probe); err != nil {
		return nil, fmt.Errorf("message: unmarshal part kind: %w", err)
	}

	switch probe.Kind {
	case "text":
		var p TextPart
		if err := json.Unmarshal(data, &p); err != nil {
			return nil, fmt.Errorf("message: unmarshal TextPart: %w", err)
		}
		return p, nil

	case "file":
		var p FilePart
		if err := json.Unmarshal(data, &p); err != nil {
			return nil, fmt.Errorf("message: unmarshal FilePart: %w", err)
		}
		return p, nil

	case "image":
		var p ImagePart
		if err := json.Unmarshal(data, &p); err != nil {
			return nil, fmt.Errorf("message: unmarshal ImagePart: %w", err)
		}
		return p, nil

	case "audio":
		var p AudioPart
		if err := json.Unmarshal(data, &p); err != nil {
			return nil, fmt.Errorf("message: unmarshal AudioPart: %w", err)
		}
		return p, nil

	case "location":
		var p LocationPart
		if err := json.Unmarshal(data, &p); err != nil {
			return nil, fmt.Errorf("message: unmarshal LocationPart: %w", err)
		}
		return p, nil

	default:
		return nil, fmt.Errorf("message: unknown part kind %q", probe.Kind)
	}
}
