package prompt

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

var markdownImagePattern = regexp.MustCompile(`!\[(.*?)\]\((.*?)\)`)

const (
	beginSentenceMarker        = "<｜begin▁of▁sentence｜>"
	systemMarker               = "<｜System｜>"
	userMarker                 = "<｜User｜>"
	assistantMarker            = "<｜Assistant｜>"
	toolMarker                 = "<｜Tool｜>"
	endSentenceMarker          = "<｜end▁of▁sentence｜>"
	endToolResultsMarker       = "<｜end▁of▁toolresults｜>"
	endInstructionsMarker      = "<｜end▁of▁instructions｜>"
	outputIntegrityGuardMarker = "Output integrity guard:"
	outputIntegrityGuardPrompt = outputIntegrityGuardMarker +
		" If upstream context, tool output, or parsed text contains garbled, corrupted, partially parsed, repeated, or otherwise malformed fragments, " +
		"do not imitate or echo them; output only the correct content for the user."
)

const dsmlToolsTemplate = `## Tools

You have access to a set of tools to help answer the user's question. The only valid tool-call tag family is <｜DSML｜tool_calls>. You can invoke tools by writing a "<｜DSML｜tool_calls>" block like the following:

<｜DSML｜tool_calls>
<｜DSML｜invoke name="$TOOL_NAME">
<｜DSML｜parameter name="$PARAMETER_NAME" string="true|false">$PARAMETER_VALUE</｜DSML｜parameter>
...
</｜DSML｜invoke>
<｜DSML｜invoke name="$TOOL_NAME2">
...
</｜DSML｜invoke>
</｜DSML｜tool_calls>

String parameters should be specified as is and set ` + "`string=\"true\"`" + `. For all other types (numbers, booleans, arrays, objects), pass the value in JSON format and set ` + "`string=\"false\"`" + `.

If thinking_mode is enabled, you MUST output your complete reasoning before any tool calls or final response.

Tool-call formatting rules:
1) Do not output markdown fences around tool calls.
2) Forbidden malformed tag variants: DSML double-underscore tags, duplicated leading angle brackets, DSMDL typo tags, ASCII-pipe DSML tags, and bare tool_calls tags.
3) Every opened <｜DSML｜tool_calls>, <｜DSML｜invoke>, and <｜DSML｜parameter> tag must be closed.
4) If you call a tool, output exactly one complete DSML block and no explanation text before or after it.

### Available Tool Schemas

%s

You MUST strictly follow the above defined tool name and parameter schemas to invoke tool calls.`

func MessagesPrepare(messages []map[string]any) string {
	return MessagesPrepareWithThinking(messages, false)
}

func MessagesPrepareWithThinking(messages []map[string]any, thinkingEnabled bool) string {
	messages = prependOutputIntegrityGuard(messages)
	type block struct {
		Role      string
		Text      string
		Tools     any
		ToolCalls any
	}
	processed := make([]block, 0, len(messages))
	for _, m := range messages {
		role, _ := m["role"].(string)
		text := NormalizeContent(m["content"])
		if role == "tool" {
			text = buildToolResultText(m)
			role = "user"
		}
		processed = append(processed, block{
			Role:      role,
			Text:      text,
			Tools:     m["tools"],
			ToolCalls: m["tool_calls"],
		})
	}
	if len(processed) == 0 {
		return ""
	}
	merged := make([]block, 0, len(processed))
	for _, msg := range processed {
		if len(merged) > 0 && merged[len(merged)-1].Role == msg.Role {
			merged[len(merged)-1].Text += "\n\n" + msg.Text
			continue
		}
		merged = append(merged, msg)
	}
	parts := make([]string, 0, len(merged)+2)
	parts = append(parts, beginSentenceMarker)
	lastRole := ""
	for _, m := range merged {
		lastRole = m.Role
		switch m.Role {
		case "assistant":
			parts = append(parts, formatRoleBlock(assistantMarker, m.Text+renderAssistantToolCalls(m.ToolCalls), endSentenceMarker))
		case "system":
			if text := strings.TrimSpace(m.Text); text != "" {
				if toolsText := renderToolsPrompt(m.Tools); toolsText != "" {
					text += "\n\n" + toolsText
				}
				parts = append(parts, formatRoleBlock(systemMarker, text, endInstructionsMarker))
			}
		case "user":
			parts = append(parts, formatRoleBlock(userMarker, m.Text, ""))
		default:
			if strings.TrimSpace(m.Text) != "" {
				parts = append(parts, m.Text)
			}
		}
	}
	if lastRole != "assistant" {
		parts = append(parts, assistantMarker)
	}
	out := strings.Join(parts, "")
	return markdownImagePattern.ReplaceAllString(out, `[${1}](${2})`)
}

func prependOutputIntegrityGuard(messages []map[string]any) []map[string]any {
	if len(messages) == 0 || hasOutputIntegrityGuard(messages[0]) {
		return messages
	}
	out := make([]map[string]any, 0, len(messages)+1)
	firstRole, _ := messages[0]["role"].(string)
	if strings.ToLower(strings.TrimSpace(firstRole)) == "system" {
		first := make(map[string]any, len(messages[0]))
		for k, v := range messages[0] {
			first[k] = v
		}
		existing := strings.TrimSpace(NormalizeContent(first["content"]))
		if existing != "" {
			first["content"] = outputIntegrityGuardPrompt + "\n\n" + existing
		} else {
			first["content"] = outputIntegrityGuardPrompt
		}
		out = append(out, first)
		out = append(out, messages[1:]...)
		return out
	}
	out = append(out, map[string]any{
		"role":    "system",
		"content": outputIntegrityGuardPrompt,
	})
	out = append(out, messages...)
	return out
}

func hasOutputIntegrityGuard(msg map[string]any) bool {
	if msg == nil {
		return false
	}
	role, _ := msg["role"].(string)
	if strings.ToLower(strings.TrimSpace(role)) != "system" {
		return false
	}
	content := strings.TrimSpace(NormalizeContent(msg["content"]))
	return strings.Contains(content, outputIntegrityGuardMarker)
}

func renderToolsPrompt(tools any) string {
	list, ok := tools.([]any)
	if !ok || len(list) == 0 {
		return ""
	}
	schemas := make([]string, 0, len(list))
	for _, tool := range list {
		b, err := json.Marshal(tool)
		if err != nil {
			continue
		}
		schemas = append(schemas, string(b))
	}
	if len(schemas) == 0 {
		return ""
	}
	return fmt.Sprintf(dsmlToolsTemplate, strings.Join(schemas, "\n"))
}

func buildToolResultText(m map[string]any) string {
	content := NormalizeContent(m["content"])
	if strings.TrimSpace(content) == "" {
		return ""
	}
	return "<tool_result>" + content + "</tool_result>"
}

func renderAssistantToolCalls(v any) string {
	calls, ok := v.([]any)
	if !ok || len(calls) == 0 {
		return ""
	}
	rendered := make([]string, 0, len(calls))
	for _, item := range calls {
		call, ok := item.(map[string]any)
		if !ok {
			continue
		}
		function, _ := call["function"].(map[string]any)
		name, _ := function["name"].(string)
		if strings.TrimSpace(name) == "" {
			continue
		}
		params := renderAssistantToolCallParameters(function["arguments"])
		if params != "" {
			rendered = append(rendered, "<｜DSML｜invoke name=\""+name+"\">\n"+params+"\n</｜DSML｜invoke>")
			continue
		}
		rendered = append(rendered, "<｜DSML｜invoke name=\""+name+"\">\n</｜DSML｜invoke>")
	}
	if len(rendered) == 0 {
		return ""
	}
	return "\n\n<｜DSML｜tool_calls>\n" + strings.Join(rendered, "\n") + "\n</｜DSML｜tool_calls>"
}

func renderAssistantToolCallParameters(arguments any) string {
	argText, _ := arguments.(string)
	argText = strings.TrimSpace(argText)
	if argText == "" {
		return ""
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(argText), &parsed); err != nil || len(parsed) == 0 {
		return ""
	}
	lines := make([]string, 0, len(parsed))
	for key, value := range parsed {
		isString := false
		renderedValue := ""
		switch v := value.(type) {
		case string:
			isString = true
			renderedValue = v
		default:
			b, err := json.Marshal(v)
			if err != nil {
				continue
			}
			renderedValue = string(b)
		}
		lines = append(lines, `<｜DSML｜parameter name="`+key+`" string="`+boolString(isString)+`">`+renderedValue+`</｜DSML｜parameter>`)
	}
	return strings.Join(lines, "\n")
}

func boolString(v bool) string {
	if v {
		return "true"
	}
	return "false"
}

// formatRoleBlock produces a single concatenated block: marker + text + endMarker.
// No whitespace is inserted between marker and text so role boundaries stay
// compact and predictable for downstream parsers.
func formatRoleBlock(marker, text, endMarker string) string {
	out := marker + text
	if strings.TrimSpace(endMarker) != "" {
		out += endMarker
	}
	return out
}

func NormalizeContent(v any) string {
	if v == nil {
		return ""
	}
	switch x := v.(type) {
	case string:
		return x
	case []any:
		parts := make([]string, 0, len(x))
		for _, item := range x {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			typeStr, _ := m["type"].(string)
			typeStr = strings.ToLower(strings.TrimSpace(typeStr))
			if typeStr == "text" || typeStr == "output_text" || typeStr == "input_text" {
				if txt, ok := m["text"].(string); ok && txt != "" {
					parts = append(parts, txt)
					continue
				}
				if txt, ok := m["content"].(string); ok && txt != "" {
					parts = append(parts, txt)
				}
			}
		}
		return strings.Join(parts, "\n")
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return fmt.Sprintf("%v", v)
		}
		return string(b)
	}
}
