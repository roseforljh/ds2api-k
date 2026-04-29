package promptcompat

import (
	"fmt"
	"regexp"
	"strings"
)

const (
	recentProgressMaxMessages  = 12
	workingStateMaxFiles       = 20
	latestObservationMaxRunes  = 480
	latestObservationMaxLines  = 4
	transcriptFilePathMaxRunes = 260
)

var (
	transcriptPathPattern = regexp.MustCompile(`(?i)([A-Z]:\\[^\s"'<>|]+|(?:\.{1,2}/)?(?:[\w.-]+[/\\])+[\w.-]+\.(?:go|js|jsx|ts|tsx|json|md|yml|yaml|sh|css|html|txt))`)
	patchFileLinePattern  = regexp.MustCompile(`(?m)^\*\*\* (?:Update|Add) File:\s*(.+?)\s*$`)
)

type agentWorkingState struct {
	ActiveGoal        string
	Status            string
	ReadFiles         []string
	ChangedFiles      []string
	LatestObservation string
	NextAction        string
	RecentMessages    []map[string]any
}

func BuildOpenAIHistoryTranscript(messages []any) string {
	return buildOpenAIFileTranscript(messages)
}

func BuildOpenAICurrentUserInputTranscript(text string) string {
	if strings.TrimSpace(text) == "" {
		return ""
	}
	return BuildOpenAICurrentInputContextTranscript([]any{
		map[string]any{"role": "user", "content": text},
	})
}

func BuildOpenAICurrentInputContextTranscript(messages []any) string {
	return buildOpenAIFileTranscript(messages)
}

func BuildOpenAIToolPromptFileTranscript(toolPrompt string) string {
	text := strings.TrimSpace(toolPrompt)
	if text == "" {
		return ""
	}
	return strings.TrimSpace("Tool instructions for the current request.\nTreat the instructions below as active system-level tool instructions.\n\n[System]\n" + text)
}

func buildOpenAIFileTranscript(messages []any) string {
	normalized := NormalizeOpenAIMessagesForPrompt(messages, "")
	if len(normalized) == 0 {
		return ""
	}
	state := buildAgentWorkingState(normalized)
	state.ReadFiles = mergeTranscriptPaths(state.ReadFiles, extractReadUIFilePathsFromMessages(messages))
	return buildActiveAgentResumeTranscript(normalized, state)
}

func buildActiveAgentResumeTranscript(messages []map[string]any, state agentWorkingState) string {
	var b strings.Builder
	b.WriteString("Request-local context package.\n\n")
	b.WriteString("Read policy:\n")
	b.WriteString("- This context belongs only to the current API request.\n")
	b.WriteString("- Answer the latest user message using ACTIVE USER GOAL, WORKING STATE, and RECENT PROGRESS only when relevant.\n")
	b.WriteString("- Do not use account-level memories, recent chats, previous sessions, or files not listed in ref_file_ids.\n")
	b.WriteString("- If the latest user message is standalone, answer it normally instead of continuing prior work.\n")
	b.WriteString("- If you need to read a file but no concrete path is available, locate it first with search/glob; never call Read with an empty file_path.\n")
	b.WriteString("- Use FULL CHRONOLOGICAL CONTEXT only as request-local reference when needed.\n\n")

	b.WriteString("=== ACTIVE USER GOAL ===\n")
	if strings.TrimSpace(state.ActiveGoal) == "" {
		b.WriteString("No explicit user goal found.\n\n")
	} else {
		b.WriteString("[User]\n")
		b.WriteString(state.ActiveGoal)
		b.WriteString("\n\n")
	}

	b.WriteString("=== WORKING STATE, READ FIRST ===\n")
	fmt.Fprintf(&b, "Status:\n- %s\n\n", nonEmptyOr(state.Status, "In progress"))
	writeStringList(&b, "Already read files:", state.ReadFiles)
	b.WriteString("\n")
	writeStringList(&b, "Already changed files:", state.ChangedFiles)
	b.WriteString("\n")
	b.WriteString("Latest observation:\n")
	fmt.Fprintf(&b, "- %s\n\n", nonEmptyOr(state.LatestObservation, "No tool observation yet."))
	b.WriteString("Next action:\n")
	fmt.Fprintf(&b, "- %s\n\n", nonEmptyOr(state.NextAction, "Continue from the latest assistant/tool message."))

	b.WriteString("=== RECENT PROGRESS, CONTINUE FROM HERE ===\n")
	if len(state.RecentMessages) == 0 {
		b.WriteString("No recent progress found.\n\n")
	} else {
		for _, msg := range state.RecentMessages {
			if formatted := formatTranscriptMessage(0, msg, false); formatted != "" {
				b.WriteString(formatted)
				b.WriteString("\n")
			}
		}
		b.WriteString("\n")
	}

	b.WriteString("=== FULL CHRONOLOGICAL CONTEXT, REFERENCE ONLY ===\n")
	for i, msg := range messages {
		if formatted := formatTranscriptMessage(i+1, msg, true); formatted != "" {
			b.WriteString(formatted)
		}
	}
	return strings.TrimSpace(b.String())
}

func buildAgentWorkingState(messages []map[string]any) agentWorkingState {
	userIndex := lastUserMessageIndex(messages)
	state := agentWorkingState{
		Status:            inferWorkingStatus(messages),
		ReadFiles:         extractFilePaths(messages),
		ChangedFiles:      extractChangedFilePaths(messages),
		LatestObservation: latestObservation(messages),
		NextAction:        "Answer the latest user message using request-local assistant/tool progress only when relevant.",
		RecentMessages:    recentProgressMessages(messages, userIndex),
	}
	if userIndex >= 0 {
		state.ActiveGoal = transcriptMessageContent(messages[userIndex])
	}
	return state
}

func lastUserMessageIndex(messages []map[string]any) int {
	for i := len(messages) - 1; i >= 0; i-- {
		if strings.EqualFold(strings.TrimSpace(asString(messages[i]["role"])), "user") && strings.TrimSpace(transcriptMessageContent(messages[i])) != "" {
			return i
		}
	}
	return -1
}

func recentProgressMessages(messages []map[string]any, userIndex int) []map[string]any {
	if len(messages) == 0 {
		return nil
	}
	start := 0
	if userIndex >= 0 {
		start = userIndex + 1
		if start >= len(messages) {
			start = userIndex
		}
	}
	progress := messages[start:]
	if len(progress) > recentProgressMaxMessages {
		progress = progress[len(progress)-recentProgressMaxMessages:]
	}
	out := make([]map[string]any, 0, len(progress))
	for _, msg := range progress {
		if strings.TrimSpace(transcriptMessageContent(msg)) != "" {
			out = append(out, msg)
		}
	}
	return out
}

func inferWorkingStatus(messages []map[string]any) string {
	if len(messages) == 0 {
		return "In progress"
	}
	last := messages[len(messages)-1]
	lastRole := strings.ToLower(strings.TrimSpace(asString(last["role"])))
	lastContent := strings.ToLower(transcriptMessageContent(last))
	switch {
	case lastRole == "tool":
		return "Reviewing latest tool result"
	case lastRole == "assistant" && strings.Contains(lastContent, "<|dsml|tool_calls>"):
		return "Waiting for tool result"
	case containsAnyFold(joinRecentMessageContent(messages, recentProgressMaxMessages), []string{"go test", "npm run build", "lint", "pytest", "测试失败", "test failed", "build failed"}):
		return "Testing"
	case containsAnyFold(joinRecentMessageContent(messages, recentProgressMaxMessages), []string{"*** update file:", "*** add file:", "applypatch", "gofmt -w", "edited", "modified"}):
		return "Editing"
	default:
		return "In progress"
	}
}

func extractFilePaths(messages []map[string]any) []string {
	return limitStrings(extractPathsFromText(joinAllMessageContent(messages)), workingStateMaxFiles)
}

func extractChangedFilePaths(messages []map[string]any) []string {
	text := joinAllMessageContent(messages)
	seen := map[string]struct{}{}
	changed := []string{}
	for _, match := range patchFileLinePattern.FindAllStringSubmatch(text, -1) {
		if len(match) < 2 {
			continue
		}
		addUniqueTranscriptPath(&changed, seen, match[1])
	}
	for _, line := range strings.Split(text, "\n") {
		lowerLine := strings.ToLower(line)
		if !containsAnyFold(lowerLine, []string{"gofmt -w", "applypatch", "*** update file:", "*** add file:"}) {
			continue
		}
		for _, path := range extractPathsFromText(line) {
			addUniqueTranscriptPath(&changed, seen, path)
		}
	}
	return limitStrings(changed, workingStateMaxFiles)
}

func latestObservation(messages []map[string]any) string {
	for i := len(messages) - 1; i >= 0; i-- {
		role := strings.ToLower(strings.TrimSpace(asString(messages[i]["role"])))
		if role != "tool" {
			continue
		}
		if content := strings.TrimSpace(transcriptMessageContent(messages[i])); content != "" {
			return summarizeLatestObservation(content)
		}
	}
	for i := len(messages) - 1; i >= 0; i-- {
		role := strings.ToLower(strings.TrimSpace(asString(messages[i]["role"])))
		if role != "assistant" {
			continue
		}
		if content := strings.TrimSpace(transcriptMessageContent(messages[i])); content != "" {
			return summarizeLatestObservation(content)
		}
	}
	return "No tool observation yet."
}

func summarizeLatestObservation(content string) string {
	content = strings.TrimSpace(content)
	if content == "" {
		return ""
	}
	lines := strings.Split(content, "\n")
	out := make([]string, 0, latestObservationMaxLines)
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		out = append(out, line)
		if len(out) >= latestObservationMaxLines {
			break
		}
	}
	if len(out) == 0 {
		return truncateMiddle(content, latestObservationMaxRunes)
	}
	summary := strings.Join(out, " | ")
	if len(out) < countNonEmptyLines(lines) {
		summary += " | ..."
	}
	return truncateMiddle(summary, latestObservationMaxRunes)
}

func countNonEmptyLines(lines []string) int {
	count := 0
	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			count++
		}
	}
	return count
}

func formatTranscriptMessage(index int, msg map[string]any, numbered bool) string {
	role := transcriptRoleLabel(asString(msg["role"]))
	content := strings.TrimSpace(transcriptMessageContent(msg))
	if content == "" {
		return ""
	}
	if numbered {
		return fmt.Sprintf("[%d] [%s]\n%s\n", index, role, content)
	}
	return fmt.Sprintf("[%s]\n%s\n", role, content)
}

func transcriptMessageContent(msg map[string]any) string {
	role := strings.ToLower(strings.TrimSpace(asString(msg["role"])))
	return strings.TrimSpace(sanitizePromptVisibleInternalToolEvents(role, NormalizeOpenAIContentForPrompt(msg["content"])))
}

func joinAllMessageContent(messages []map[string]any) string {
	return joinRecentMessageContent(messages, len(messages))
}

func joinRecentMessageContent(messages []map[string]any, maxMessages int) string {
	if maxMessages <= 0 || maxMessages > len(messages) {
		maxMessages = len(messages)
	}
	start := len(messages) - maxMessages
	parts := make([]string, 0, maxMessages)
	for _, msg := range messages[start:] {
		if content := transcriptMessageContent(msg); content != "" {
			parts = append(parts, content)
		}
	}
	return strings.Join(parts, "\n")
}

func extractPathsFromText(text string) []string {
	seen := map[string]struct{}{}
	out := []string{}
	for _, match := range transcriptPathPattern.FindAllString(text, -1) {
		addUniqueTranscriptPath(&out, seen, match)
	}
	return out
}

func extractReadUIFilePathsFromMessages(messages []any) []string {
	seen := map[string]struct{}{}
	out := []string{}
	for _, raw := range messages {
		msg, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		role := strings.ToLower(strings.TrimSpace(asString(msg["role"])))
		switch role {
		case "assistant", "tool", "function":
		default:
			continue
		}
		lines := strings.Split(NormalizeOpenAIContentForPrompt(msg["content"]), "\n")
		afterReadUI := false
		for _, line := range lines {
			trimmed := strings.TrimSpace(line)
			if internalToolUILinePattern.MatchString(trimmed) {
				afterReadUI = true
				continue
			}
			if !afterReadUI {
				continue
			}
			if !internalToolResultUILinePattern.MatchString(trimmed) {
				if trimmed != "" {
					afterReadUI = false
				}
				continue
			}
			for _, path := range extractPathsFromText(trimmed) {
				addUniqueTranscriptPath(&out, seen, path)
			}
		}
	}
	return out
}

func mergeTranscriptPaths(primary, secondary []string) []string {
	seen := map[string]struct{}{}
	out := []string{}
	for _, path := range primary {
		addUniqueTranscriptPath(&out, seen, path)
	}
	for _, path := range secondary {
		addUniqueTranscriptPath(&out, seen, path)
	}
	return limitStrings(out, workingStateMaxFiles)
}

func addUniqueTranscriptPath(out *[]string, seen map[string]struct{}, raw string) {
	path := cleanTranscriptPath(raw)
	if path == "" {
		return
	}
	key := strings.ToLower(path)
	if _, ok := seen[key]; ok {
		return
	}
	seen[key] = struct{}{}
	*out = append(*out, path)
}

func cleanTranscriptPath(path string) string {
	path = strings.TrimSpace(path)
	path = strings.Trim(path, "`\"'<>")
	path = strings.TrimRight(path, ".,;:)]}")
	if len([]rune(path)) > transcriptFilePathMaxRunes {
		return ""
	}
	if !strings.Contains(path, ".") {
		return ""
	}
	return path
}

func writeStringList(b *strings.Builder, title string, items []string) {
	b.WriteString(title)
	b.WriteString("\n")
	if len(items) == 0 {
		b.WriteString("- none\n")
		return
	}
	for _, item := range items {
		if strings.TrimSpace(item) == "" {
			continue
		}
		fmt.Fprintf(b, "- %s\n", item)
	}
}

func limitStrings(items []string, max int) []string {
	if max <= 0 || len(items) <= max {
		return items
	}
	return items[:max]
}

func truncateMiddle(text string, maxRunes int) string {
	text = strings.TrimSpace(text)
	runes := []rune(text)
	if maxRunes <= 0 || len(runes) <= maxRunes {
		return text
	}
	head := maxRunes / 2
	tail := maxRunes - head
	return string(runes[:head]) + "\n...[truncated]...\n" + string(runes[len(runes)-tail:])
}

func containsAnyFold(text string, needles []string) bool {
	text = strings.ToLower(text)
	for _, needle := range needles {
		if strings.Contains(text, strings.ToLower(needle)) {
			return true
		}
	}
	return false
}

func nonEmptyOr(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func transcriptRoleLabel(role string) string {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "system":
		return "System"
	case "assistant":
		return "Assistant"
	case "tool":
		return "Tool"
	case "user":
		return "User"
	default:
		if role == "" {
			return "Message"
		}
		return strings.ToUpper(role[:1]) + strings.ToLower(role[1:])
	}
}
