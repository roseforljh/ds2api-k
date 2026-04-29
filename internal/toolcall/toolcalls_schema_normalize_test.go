package toolcall

import (
	"reflect"
	"testing"
)

func TestNormalizeParsedToolCallsForSchemasCoercesDeclaredStringFieldsRecursively(t *testing.T) {
	toolsRaw := []any{
		map[string]any{
			"type": "function",
			"function": map[string]any{
				"name": "TaskUpdate",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"taskId": map[string]any{"type": "string"},
						"payload": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"content": map[string]any{"type": "string"},
								"tags": map[string]any{
									"type":  "array",
									"items": map[string]any{"type": "string"},
								},
								"count": map[string]any{"type": "number"},
							},
						},
					},
				},
			},
		},
	}
	calls := []ParsedToolCall{{
		Name: "TaskUpdate",
		Input: map[string]any{
			"taskId": 1,
			"payload": map[string]any{
				"content": map[string]any{"text": "hello"},
				"tags":    []any{1, true, map[string]any{"k": "v"}},
				"count":   2,
			},
		},
	}}

	got := NormalizeParsedToolCallsForSchemas(calls, toolsRaw)
	if len(got) != 1 {
		t.Fatalf("expected one normalized call, got %#v", got)
	}
	if got[0].Input["taskId"] != "1" {
		t.Fatalf("expected taskId coerced to string, got %#v", got[0].Input["taskId"])
	}
	payload, ok := got[0].Input["payload"].(map[string]any)
	if !ok {
		t.Fatalf("expected payload object, got %#v", got[0].Input["payload"])
	}
	if payload["content"] != `{"text":"hello"}` {
		t.Fatalf("expected nested content coerced to json string, got %#v", payload["content"])
	}
	if payload["count"] != 2 {
		t.Fatalf("expected non-string count unchanged, got %#v", payload["count"])
	}
	tags, ok := payload["tags"].([]any)
	if !ok {
		t.Fatalf("expected tags slice, got %#v", payload["tags"])
	}
	wantTags := []any{"1", "true", `{"k":"v"}`}
	if !reflect.DeepEqual(tags, wantTags) {
		t.Fatalf("unexpected normalized tags: got %#v want %#v", tags, wantTags)
	}
}

func TestNormalizeParsedToolCallsForSchemasSupportsDirectToolSchemaShape(t *testing.T) {
	toolsRaw := []any{
		map[string]any{
			"name": "Write",
			"input_schema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"content": map[string]any{"type": "string"},
				},
			},
		},
	}
	calls := []ParsedToolCall{{Name: "Write", Input: map[string]any{"content": []any{"a", 1}}}}
	got := NormalizeParsedToolCallsForSchemas(calls, toolsRaw)
	if got[0].Input["content"] != `["a",1]` {
		t.Fatalf("expected direct-schema content coerced to string, got %#v", got[0].Input["content"])
	}
}

func TestNormalizeParsedToolCallsForSchemasSupportsInputSchemaAndSchemaAliases(t *testing.T) {
	toolsRaw := []any{
		map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        "Write",
				"description": "write content",
				"inputSchema": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"content": map[string]any{"type": "string"},
					},
				},
			},
		},
		map[string]any{
			"name": "Patch",
			"schema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"body": map[string]any{"type": "string"},
				},
			},
		},
	}
	calls := []ParsedToolCall{
		{Name: "Write", Input: map[string]any{"content": []any{"a", 1}}},
		{Name: "Patch", Input: map[string]any{"body": map[string]any{"k": "v"}}},
	}

	got := NormalizeParsedToolCallsForSchemas(calls, toolsRaw)
	if got[0].Input["content"] != `["a",1]` {
		t.Fatalf("expected inputSchema alias to coerce content, got %#v", got[0].Input["content"])
	}
	if got[1].Input["body"] != `{"k":"v"}` {
		t.Fatalf("expected schema alias to coerce body, got %#v", got[1].Input["body"])
	}

	name, desc, schema := ExtractToolMeta(toolsRaw[0].(map[string]any))
	if name != "Write" || desc != "write content" || schema == nil {
		t.Fatalf("unexpected extracted metadata: name=%q desc=%q schema=%#v", name, desc, schema)
	}
}

func TestNormalizeParsedToolCallsForSchemasLeavesAmbiguousUnionUnchanged(t *testing.T) {
	toolsRaw := []any{
		map[string]any{
			"type": "function",
			"function": map[string]any{
				"name": "TaskUpdate",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"taskId": map[string]any{"type": []any{"string", "integer"}},
					},
				},
			},
		},
	}
	calls := []ParsedToolCall{{Name: "TaskUpdate", Input: map[string]any{"taskId": 1}}}
	got := NormalizeParsedToolCallsForSchemas(calls, toolsRaw)
	if got[0].Input["taskId"] != 1 {
		t.Fatalf("expected ambiguous union to stay unchanged, got %#v", got[0].Input["taskId"])
	}
}

func TestNormalizeParsedToolCallsForSchemasParsesDeclaredObjectString(t *testing.T) {
	toolsRaw := []any{
		map[string]any{
			"type": "function",
			"function": map[string]any{
				"name": "submit_work",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"files": map[string]any{"type": "object"},
					},
				},
			},
		},
	}
	calls := []ParsedToolCall{{Name: "submit_work", Input: map[string]any{"files": `{"hello.txt":"world"}`}}}
	got := NormalizeParsedToolCallsForSchemas(calls, toolsRaw)
	files, ok := got[0].Input["files"].(map[string]any)
	if !ok || files["hello.txt"] != "world" {
		t.Fatalf("expected JSON string files to become object, got %#v", got[0].Input["files"])
	}
}
