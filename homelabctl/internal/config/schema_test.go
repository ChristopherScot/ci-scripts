package config

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestSchemaIsValidJSON(t *testing.T) {
	var v any
	if err := json.Unmarshal([]byte(Schema), &v); err != nil {
		t.Fatalf("Schema is not valid JSON: %v", err)
	}
}

// The schema is hand-maintained, so it can drift from the struct. A field
// the struct accepts but the schema omits gets flagged as an unknown key
// in the editor - the opposite of the point.
func TestSchemaCoversConfig(t *testing.T) {
	var raw map[string]any
	if err := json.Unmarshal([]byte(Schema), &raw); err != nil {
		t.Fatal(err)
	}
	props, _ := raw["properties"].(map[string]any)
	if len(props) == 0 {
		t.Fatal("schema has no properties")
	}

	tt := reflect.TypeOf(Config{})
	for i := 0; i < tt.NumField(); i++ {
		tag := tt.Field(i).Tag.Get("yaml")
		name := strings.Split(tag, ",")[0]
		if name == "" || name == "-" {
			continue // not settable from YAML
		}
		if _, ok := props[name]; !ok {
			t.Errorf("config field %q is accepted from YAML but missing from Schema", name)
		}
	}

}
