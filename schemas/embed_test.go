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

func TestCLIResultsAreAConstrainedUnion(t *testing.T) {
	raw, ok := Read("cli")
	if !ok {
		t.Fatal("CLI schema is missing")
	}
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	properties, _ := document["properties"].(map[string]any)
	results, _ := properties["results"].(map[string]any)
	items, _ := results["items"].(map[string]any)
	union, _ := items["oneOf"].([]any)
	if len(union) != 3 {
		t.Fatalf("CLI results must constrain connection, service, and import items: %#v", results)
	}
}
