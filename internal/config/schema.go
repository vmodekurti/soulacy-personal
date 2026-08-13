package config

import (
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// ValidateSchemaFile rejects keys that cannot affect the real Config type.
// Explicit extension maps (channels, plugins, provider configs, MCP env and
// headers) remain open by design; their containing keys are still checked.
func ValidateSchemaFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return ValidateSchema(data)
}

func ValidateSchema(data []byte) error {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return err
	}
	if len(doc.Content) == 0 {
		return nil
	}
	return validateSchemaNode(doc.Content[0], reflect.TypeOf(Config{}), "")
}

func validateSchemaNode(node *yaml.Node, typ reflect.Type, path string) error {
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	if node == nil {
		return nil
	}
	switch typ.Kind() {
	case reflect.Struct:
		if node.Kind != yaml.MappingNode {
			return nil
		}
		fields := schemaFields(typ)
		for i := 0; i+1 < len(node.Content); i += 2 {
			key, val := node.Content[i].Value, node.Content[i+1]
			childPath := key
			if path != "" {
				childPath = path + "." + key
			}
			if childPath == "runtime.allow_system_tools" {
				return fmt.Errorf("unknown security setting %q: use runtime.allow_system_agents (an explicit agent-ID list)", childPath)
			}
			fieldType, ok := fields[key]
			if !ok {
				return fmt.Errorf("unknown configuration key %q", childPath)
			}
			if err := validateSchemaNode(val, fieldType, childPath); err != nil {
				return err
			}
		}
	case reflect.Map:
		if typ.Elem().Kind() == reflect.Interface {
			return nil
		}
		if node.Kind != yaml.MappingNode {
			return nil
		}
		for i := 0; i+1 < len(node.Content); i += 2 {
			if err := validateSchemaNode(node.Content[i+1], typ.Elem(), path+"."+node.Content[i].Value); err != nil {
				return err
			}
		}
	case reflect.Slice, reflect.Array:
		if node.Kind != yaml.SequenceNode {
			return nil
		}
		for _, item := range node.Content {
			if err := validateSchemaNode(item, typ.Elem(), path); err != nil {
				return err
			}
		}
	}
	return nil
}

func schemaFields(typ reflect.Type) map[string]reflect.Type {
	out := map[string]reflect.Type{}
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		name := strings.Split(f.Tag.Get("mapstructure"), ",")[0]
		if name == "" {
			name = strings.ToLower(f.Name)
		}
		if name != "-" {
			out[name] = f.Type
		}
	}
	return out
}

// SecuritySchemaKeys feeds documentation/UI contract tests.
func SecuritySchemaKeys() []string {
	var out []string
	for _, entry := range []struct {
		prefix string
		typ    reflect.Type
	}{
		{"server", reflect.TypeOf(ServerConfig{})}, {"runtime", reflect.TypeOf(RuntimeConfig{})},
		{"auth", reflect.TypeOf(AuthConfig{})}, {"security", reflect.TypeOf(SecurityConfig{})},
		{"credentials", reflect.TypeOf(CredentialsConfig{})},
	} {
		for key := range schemaFields(entry.typ) {
			out = append(out, entry.prefix+"."+key)
		}
	}
	sort.Strings(out)
	return out
}
