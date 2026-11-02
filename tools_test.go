package main

import (
	"encoding/json"
	"testing"
)

func TestCompactToolSurface(t *testing.T) {
	want := map[string]bool{"get_user": true, "get_post": true, "list_post_comments": true, "get_feed": true, "search": true}
	for _, tool := range readTools() {
		if !want[tool.Name] {
			t.Fatalf("unexpected or duplicate tool %q", tool.Name)
		}
		delete(want, tool.Name)
		if !tool.Annotations.ReadOnlyHint || !tool.Annotations.IdempotentHint || tool.Annotations.DestructiveHint {
			t.Fatalf("unsafe annotations: %+v", tool)
		}
		schema := tool.InputSchema.(map[string]any)
		if schema["type"] != "object" || schema["additionalProperties"] != false {
			t.Fatalf("loose schema: %+v", tool)
		}
		if _, err := json.Marshal(tool); err != nil {
			t.Fatal(err)
		}
	}
	if len(want) != 0 {
		t.Fatalf("missing tools: %v", want)
	}
	data, _ := json.Marshal(readTools())
	t.Logf("Advertised tool definitions: %d JSON bytes", len(data))
}
