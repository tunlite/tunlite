package schemas

import (
	"encoding/json"
	"testing"
)

func TestEmbeddedSchemasAreValidJSON(t *testing.T) {
	for name := range Names {
		raw, ok := Read(name)
		if !ok {
			t.Fatalf("schema %q is missing", name)
		}
		var document map[string]any
		if err := json.Unmarshal(raw, &document); err != nil {
			t.Fatalf("schema %q is invalid JSON: %v", name, err)
		}
		if document["$schema"] != "https://json-schema.org/draft/2020-12/schema" {
			t.Fatalf("schema %q has no draft identifier", name)
		}
	}
}
