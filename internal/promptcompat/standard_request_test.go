package promptcompat

import "testing"

func TestStandardRequestCompletionPayloadSetsModelTypeFromResolvedModel(t *testing.T) {
	tests := []struct {
		name      string
		model     string
		thinking  bool
		search    bool
		modelType string
	}{
		{name: "default", model: "deepseek-v4-flash", thinking: false, search: false, modelType: "default"},
		{name: "default_nothinking", model: "deepseek-v4-flash-nothinking", thinking: false, search: false, modelType: "default"},
		{name: "expert", model: "deepseek-v4-pro", thinking: true, search: false, modelType: "expert"},
		{name: "vision", model: "deepseek-v4-vision-search", thinking: false, search: true, modelType: "vision"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := StandardRequest{
				ResolvedModel: tc.model,
				FinalPrompt:   "hello",
				Thinking:      tc.thinking,
				Search:        tc.search,
				RefFileIDs:    []string{"file-a", "file-b"},
				PassThrough: map[string]any{
					"temperature": 0.3,
				},
			}

			payload := req.CompletionPayload("session-123")

			if got := payload["model_type"]; got != tc.modelType {
				t.Fatalf("expected model_type %s, got %#v", tc.modelType, got)
			}
			if got := payload["chat_session_id"]; got != "session-123" {
				t.Fatalf("unexpected chat_session_id: %#v", got)
			}
			if got := payload["thinking_enabled"]; got != tc.thinking {
				t.Fatalf("unexpected thinking_enabled: %#v", got)
			}
			if got := payload["search_enabled"]; got != tc.search {
				t.Fatalf("unexpected search_enabled: %#v", got)
			}
			if got := payload["temperature"]; got != 0.3 {
				t.Fatalf("expected passthrough temperature, got %#v", got)
			}
			refFileIDs, ok := payload["ref_file_ids"].([]any)
			if !ok {
				t.Fatalf("expected ref_file_ids slice, got %#v", payload["ref_file_ids"])
			}
			if len(refFileIDs) != 2 || refFileIDs[0] != "file-a" || refFileIDs[1] != "file-b" {
				t.Fatalf("unexpected ref_file_ids: %#v", refFileIDs)
			}
		})
	}
}

func TestNormalizeOpenAIChatRequestPromotesImageInputToVisionModel(t *testing.T) {
	req := map[string]any{
		"model": "gpt-4o",
		"messages": []any{
			map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{"type": "text", "text": "describe this image"},
					map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,abc"}},
				},
			},
		},
	}

	stdReq, err := NormalizeOpenAIChatRequest(nil, req, "")
	if err != nil {
		t.Fatalf("normalize failed: %v", err)
	}
	if stdReq.ResolvedModel != "deepseek-v4-vision" {
		t.Fatalf("expected image input to promote model to vision, got %q", stdReq.ResolvedModel)
	}
	payload := stdReq.CompletionPayload("session-123")
	if payload["model_type"] != "vision" {
		t.Fatalf("expected vision model_type, got %#v", payload["model_type"])
	}
}

func TestNormalizeOpenAIChatRequestPromotesImageAttachmentToVisionSearchModel(t *testing.T) {
	req := map[string]any{
		"model": "deepseek-v4-flash-search-nothinking",
		"messages": []any{
			map[string]any{"role": "user", "content": "describe attached image"},
		},
		"attachments": []any{
			map[string]any{"file_id": "file-image", "is_image": true},
		},
	}

	stdReq, err := NormalizeOpenAIChatRequest(nil, req, "")
	if err != nil {
		t.Fatalf("normalize failed: %v", err)
	}
	if stdReq.ResolvedModel != "deepseek-v4-vision-search-nothinking" {
		t.Fatalf("expected image attachment to preserve search/nothinking while promoting to vision, got %q", stdReq.ResolvedModel)
	}
	if stdReq.Thinking {
		t.Fatalf("expected nothinking variant to keep thinking disabled")
	}
	if !stdReq.Search {
		t.Fatalf("expected search flag preserved")
	}
	payload := stdReq.CompletionPayload("session-123")
	if payload["model_type"] != "vision" {
		t.Fatalf("expected vision model_type, got %#v", payload["model_type"])
	}
}

func TestNormalizeOpenAIResponsesRequestPromotesInputImageToVisionModel(t *testing.T) {
	req := map[string]any{
		"model": "gpt-4.1",
		"input": []any{
			map[string]any{
				"type": "message",
				"role": "user",
				"content": []any{
					map[string]any{"type": "input_text", "text": "what is in this image?"},
					map[string]any{"type": "input_image", "image_url": "data:image/jpeg;base64,abc"},
				},
			},
		},
	}

	stdReq, err := NormalizeOpenAIResponsesRequest(nil, req, "")
	if err != nil {
		t.Fatalf("normalize failed: %v", err)
	}
	if stdReq.ResolvedModel != "deepseek-v4-vision" {
		t.Fatalf("expected responses image input to promote model to vision, got %q", stdReq.ResolvedModel)
	}
}
