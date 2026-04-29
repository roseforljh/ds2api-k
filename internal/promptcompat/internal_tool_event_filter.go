package promptcompat

import (
	"regexp"
	"strings"
)

var (
	internalTaskEventLinePattern           = regexp.MustCompile(`^\s*(?:\[[A-Za-z]*Task[A-Za-z]*Done\]|\[Task[A-Za-z]*Done\])(?:\s|$)`)
	internalTaskCreatedLinePattern         = regexp.MustCompile(`^\s*(?:\[[^\]]+\]\s*)?Task\s+#\d+\s+created\b`)
	internalTaskProgressLinePattern        = regexp.MustCompile(`^\s*(?:\[[^\]]+\]\s*)?Task\s+#\d+\s+is\s+now\s+\S+`)
	internalTaskUpdatedLinePattern         = regexp.MustCompile(`^\s*Updated\s+task\s+#\d+\s+status\b`)
	internalToolTranscriptLinePattern      = regexp.MustCompile(`^\s*(?:●\s*)?(?:\[?\d+\]?|\d+\])\s*\[Tool\]\s*$`)
	internalAssistantTranscriptLinePattern = regexp.MustCompile(`^\s*(?:●\s*)?(?:\[?\d+\]?|\d+\])\s*\[Assistant\]\s*(.*)$`)
	internalBareToolRoleLinePattern        = regexp.MustCompile(`^\s*\[Tool\]\s*$`)
	internalToolUILinePattern              = regexp.MustCompile(`^\s*(?:●\s*)?(?:Read|Reading)\s+\d+\s+file(?:s)?\b`)
	internalToolResultUILinePattern        = regexp.MustCompile(`^\s*⎿\s+`)
	internalToolErrorLinePattern           = regexp.MustCompile(`^\s*(?:String to replace not found in file\.|String:\s|Error:\s|File .* has been modified externally|The file .* has been updated successfully)`)
	internalProgressUILinePattern          = regexp.MustCompile(`^\s*[✽⎿]\s*(?:Processing|Tip:)`)
	internalTaskOrdinalLinePattern         = regexp.MustCompile(`^\s*\[Task(?:Create|Update|Delete|Complete|Cancel)[A-Za-z]*\](?:\s|$)`)
)

func sanitizePromptVisibleInternalToolEvents(role, content string) string {
	role = strings.ToLower(strings.TrimSpace(role))
	switch role {
	case "assistant", "tool", "function":
	default:
		return content
	}
	if strings.TrimSpace(content) == "" {
		return content
	}
	lines := strings.Split(content, "\n")
	out := make([]string, 0, len(lines))
	skipToolBlock := false
	for _, line := range lines {
		if text, ok := assistantTranscriptText(line); ok {
			skipToolBlock = false
			if strings.TrimSpace(text) != "" {
				out = append(out, text)
			}
			continue
		}
		if isInternalToolEventLeakLine(line) {
			skipToolBlock = internalToolTranscriptLinePattern.MatchString(strings.TrimSpace(line)) ||
				internalBareToolRoleLinePattern.MatchString(strings.TrimSpace(line)) ||
				internalToolErrorLinePattern.MatchString(strings.TrimSpace(line))
			continue
		}
		if skipToolBlock {
			continue
		}
		out = append(out, line)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

func assistantTranscriptText(line string) (string, bool) {
	match := internalAssistantTranscriptLinePattern.FindStringSubmatch(strings.TrimSpace(line))
	if len(match) < 2 {
		return "", false
	}
	return strings.TrimSpace(match[1]), true
}

func isInternalToolEventLeakLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return false
	}
	return internalTaskEventLinePattern.MatchString(trimmed) ||
		internalTaskOrdinalLinePattern.MatchString(trimmed) ||
		internalTaskCreatedLinePattern.MatchString(trimmed) ||
		internalTaskProgressLinePattern.MatchString(trimmed) ||
		internalTaskUpdatedLinePattern.MatchString(trimmed) ||
		internalToolTranscriptLinePattern.MatchString(trimmed) ||
		internalBareToolRoleLinePattern.MatchString(trimmed) ||
		internalToolUILinePattern.MatchString(trimmed) ||
		internalToolResultUILinePattern.MatchString(trimmed) ||
		internalToolErrorLinePattern.MatchString(trimmed) ||
		internalProgressUILinePattern.MatchString(trimmed)
}
