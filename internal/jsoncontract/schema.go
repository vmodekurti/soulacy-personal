// Package jsoncontract enforces declared JSON contracts independently of a model.
package jsoncontract

import (
	"encoding/json"
	"fmt"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// Validate checks a JSON-compatible Go value against a self-contained schema.
// Normalize typed Go slices/numbers as JSON before handing them to the validator.
// References within the schema are allowed; remote and filesystem loads are not.
func Validate(schema map[string]any, value any) error {
	if schema == nil {
		return nil
	}
	doc, err := normalize(schema)
	if err != nil {
		return fmt.Errorf("invalid schema: %w", err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.UseLoader(localOnly{})
	const resource = "https://soulacy.invalid/contract.json"
	if err := compiler.AddResource(resource, doc); err != nil {
		return err
	}
	compiled, err := compiler.Compile(resource)
	if err != nil {
		return fmt.Errorf("invalid schema: %w", err)
	}
	instance, err := normalize(value)
	if err != nil {
		return fmt.Errorf("invalid JSON value: %w", err)
	}
	return compiled.Validate(instance)
}

func normalize(value any) (any, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var out any
	err = json.Unmarshal(data, &out)
	return out, err
}

type localOnly struct{}

func (localOnly) Load(url string) (any, error) {
	return nil, fmt.Errorf("external schema reference %q is not allowed; embed definitions in the schema", url)
}
