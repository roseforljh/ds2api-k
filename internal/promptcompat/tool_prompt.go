package promptcompat

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"ds2api/internal/toolcall"
)

func injectToolPrompt(messages []map[string]any, tools []any, policy ToolChoicePolicy) ([]map[string]any, []string) {
	toolPrompt, names := BuildOpenAIToolPrompt(tools, policy)
	if strings.TrimSpace(toolPrompt) == "" {
		return messages, names
	}

	wrapped := "=== TOOL INSTRUCTIONS, MUST FOLLOW ===\n" + strings.TrimSpace(toolPrompt) + "\n=== END TOOL INSTRUCTIONS ==="
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i]["role"] == "user" {
			old := strings.TrimSpace(normalizeToolPromptTargetContent(messages[i]["content"]))
			messages[i]["content"] = strings.TrimSpace(old + "\n\n" + wrapped)
			return messages, names
		}
	}
	messages = append(messages, map[string]any{"role": "user", "content": wrapped})
	return messages, names
}

func normalizeToolPromptTargetContent(v any) string {
	if text, ok := v.(string); ok {
		return text
	}
	encoded, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(encoded)
}

func BuildOpenAIToolPrompt(toolsRaw any, policy ToolChoicePolicy) (string, []string) {
	if policy.IsNone() {
		return "", nil
	}
	tools, ok := toolsRaw.([]any)
	if !ok || len(tools) == 0 {
		return "", nil
	}
	toolSchemas := make([]string, 0, len(tools))
	names := make([]string, 0, len(tools))
	isAllowed := func(name string) bool {
		if strings.TrimSpace(name) == "" {
			return false
		}
		if len(policy.Allowed) == 0 {
			return true
		}
		_, ok := policy.Allowed[name]
		return ok
	}

	for _, t := range tools {
		tool, ok := t.(map[string]any)
		if !ok {
			continue
		}
		name, desc, schema := toolcall.ExtractToolMeta(tool)
		name = strings.TrimSpace(name)
		if !isAllowed(name) {
			continue
		}
		names = append(names, name)
		if desc == "" {
			desc = "No description available"
		}
		b, _ := json.Marshal(schema)
		schemaBlock := fmt.Sprintf("Tool: %s\nDescription: %s\nParameters: %s", name, desc, string(b))
		if summary := buildToolParameterSummary(name, schema); summary != "" {
			schemaBlock += "\n" + summary
		}
		toolSchemas = append(toolSchemas, schemaBlock)
	}
	if len(toolSchemas) == 0 {
		return "", names
	}
	toolPrompt := "You have access to these tools:\n\n" + strings.Join(toolSchemas, "\n\n") + "\n\n" + toolcall.BuildToolCallInstructions(names)
	if policy.Mode == ToolChoiceRequired {
		toolPrompt += "\n7) For this response, you MUST call at least one tool from the allowed list."
	}
	if policy.Mode == ToolChoiceForced && strings.TrimSpace(policy.ForcedName) != "" {
		toolPrompt += "\n7) For this response, you MUST call exactly this tool name: " + strings.TrimSpace(policy.ForcedName)
		toolPrompt += "\n8) Do not call any other tool."
	}

	return toolPrompt, names
}

func buildToolParameterSummary(name string, schema any) string {
	schemaMap, ok := schema.(map[string]any)
	if !ok || len(schemaMap) == 0 {
		return ""
	}
	properties, _ := schemaMap["properties"].(map[string]any)
	if len(properties) == 0 {
		return ""
	}
	required := schemaRequiredNames(schemaMap["required"])
	requiredSet := map[string]struct{}{}
	for _, name := range required {
		requiredSet[name] = struct{}{}
	}
	optional := make([]string, 0, len(properties))
	for param := range properties {
		if _, ok := requiredSet[param]; ok {
			continue
		}
		optional = append(optional, param)
	}
	sort.Strings(required)
	sort.Strings(optional)

	var b strings.Builder
	fmt.Fprintf(&b, "Parameter summary for %s:\n", name)
	if len(required) > 0 {
		b.WriteString("Required parameters:\n")
		for _, param := range required {
			fmt.Fprintf(&b, "- %s\n", param)
		}
	} else {
		b.WriteString("Required parameters:\n- none\n")
	}
	if len(optional) > 0 {
		b.WriteString("Optional parameters:\n")
		for _, param := range optional {
			fmt.Fprintf(&b, "- %s\n", param)
		}
	}
	if len(required) > 0 {
		fmt.Fprintf(&b, "Never call %s without: %s.", name, strings.Join(required, ", "))
	}
	return strings.TrimSpace(b.String())
}

func schemaRequiredNames(raw any) []string {
	switch values := raw.(type) {
	case []any:
		out := make([]string, 0, len(values))
		for _, item := range values {
			if s := strings.TrimSpace(asString(item)); s != "" {
				out = append(out, s)
			}
		}
		return out
	case []string:
		out := make([]string, 0, len(values))
		for _, item := range values {
			if s := strings.TrimSpace(item); s != "" {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}
