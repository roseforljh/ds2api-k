package promptcompat

import "strings"

func requestHasImageInput(req map[string]any, messages []any) bool {
	if hasImageInput(req) {
		return true
	}
	if len(messages) > 0 {
		return hasImageInput(messages)
	}
	return false
}

func hasImageInput(raw any) bool {
	switch x := raw.(type) {
	case string:
		return strings.HasPrefix(strings.ToLower(strings.TrimSpace(x)), "data:image/")
	case []any:
		for _, item := range x {
			if hasImageInput(item) {
				return true
			}
		}
	case map[string]any:
		typeStr := strings.ToLower(strings.TrimSpace(asString(x["type"])))
		switch typeStr {
		case "image", "image_url", "input_image", "image_file":
			return true
		}
		for _, key := range []string{"mime_type", "media_type", "content_type"} {
			if strings.HasPrefix(strings.ToLower(strings.TrimSpace(asString(x[key]))), "image/") {
				return true
			}
		}
		if isBoolTrue(x["is_image"]) || isBoolTrue(x["isImage"]) {
			return true
		}
		for _, key := range []string{"image_url", "image_file", "file", "source", "content", "input", "items", "data", "attachments", "messages", "files"} {
			if nested, ok := x[key]; ok && hasImageInput(nested) {
				return true
			}
		}
	}
	return false
}

func isBoolTrue(v any) bool {
	b, ok := v.(bool)
	return ok && b
}

func visionModelVariant(model string) string {
	model = strings.ToLower(strings.TrimSpace(model))
	if model == "" {
		return model
	}
	noThinking := strings.HasSuffix(model, "-nothinking")
	base := strings.TrimSuffix(model, "-nothinking")
	search := strings.Contains(base, "-search")
	if search {
		base = "deepseek-v4-vision-search"
	} else {
		base = "deepseek-v4-vision"
	}
	if noThinking {
		return base + "-nothinking"
	}
	return base
}
