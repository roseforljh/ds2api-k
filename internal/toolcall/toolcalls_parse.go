package toolcall

import (
	"strings"
)

type ParsedToolCall struct {
	Name  string         `json:"name"`
	Input map[string]any `json:"input"`
}

type ToolCallParseResult struct {
	Calls             []ParsedToolCall
	SawToolCallSyntax bool
	RejectedByPolicy  bool
	RejectedToolNames []string
	RejectedInvalid   bool
}

func ParseToolCalls(text string, availableToolNames []string) []ParsedToolCall {
	return ParseToolCallsDetailed(text, availableToolNames).Calls
}

func ParseToolCallsDetailed(text string, availableToolNames []string) ToolCallParseResult {
	return parseToolCallsDetailedXMLOnly(text, availableToolNames)
}

func ParseStandaloneToolCalls(text string, availableToolNames []string) []ParsedToolCall {
	return ParseStandaloneToolCallsDetailed(text, availableToolNames).Calls
}

func ParseStandaloneToolCallsDetailed(text string, availableToolNames []string) ToolCallParseResult {
	return parseToolCallsDetailedXMLOnly(text, availableToolNames)
}

func ParseAssistantToolCallsDetailed(text, thinking string, availableToolNames []string) ToolCallParseResult {
	textParsed := ParseStandaloneToolCallsDetailed(text, availableToolNames)
	if len(textParsed.Calls) > 0 {
		return textParsed
	}
	if strings.TrimSpace(text) != "" {
		return textParsed
	}
	thinkingParsed := ParseStandaloneToolCallsDetailed(thinking, availableToolNames)
	if len(thinkingParsed.Calls) > 0 {
		return thinkingParsed
	}
	return textParsed
}

func parseToolCallsDetailedXMLOnly(text string, availableToolNames []string) ToolCallParseResult {
	result := ToolCallParseResult{}
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return result
	}
	result.SawToolCallSyntax = looksLikeToolCallSyntax(trimmed)
	trimmed = stripFencedCodeBlocks(trimmed)
	trimmed = strings.TrimSpace(trimmed)
	if trimmed == "" {
		return result
	}
	if ContainsDSMLCallsAliasOutsideIgnored(trimmed) || ContainsCJKDSMLAliasOutsideIgnored(trimmed) {
		result.SawToolCallSyntax = true
		result.RejectedInvalid = true
		return result
	}

	normalized, ok := normalizeDSMLToolCallMarkup(trimmed)
	if !ok {
		if LooksLikeMalformedRequiredToolCall(trimmed) || LooksLikeUnsafeStructuredToolIntent(trimmed, availableToolNames) {
			result.SawToolCallSyntax = true
			result.RejectedInvalid = true
		}
		return result
	}
	if looksLikeHashDSMLPseudoToolSyntax(trimmed) && LooksLikeMalformedRequiredToolCall(trimmed) {
		result.RejectedInvalid = true
		return result
	}
	parsed := parseXMLToolCalls(normalized)
	if len(parsed) == 0 && strings.Contains(strings.ToLower(normalized), "<![cdata[") {
		recovered := SanitizeLooseCDATA(normalized)
		if recovered != normalized {
			parsed = parseXMLToolCalls(recovered)
		}
	}
	if len(parsed) == 0 {
		if LooksLikeMalformedRequiredToolCall(trimmed) || LooksLikeUnsafeStructuredToolIntent(trimmed, availableToolNames) {
			result.SawToolCallSyntax = true
			result.RejectedInvalid = true
		}
		return result
	}

	result.SawToolCallSyntax = true
	calls, rejectedNames, rejectedInvalid := filterToolCallsDetailed(parsed, availableToolNames)
	result.Calls = calls
	result.RejectedToolNames = rejectedNames
	result.RejectedByPolicy = len(rejectedNames) > 0 && len(calls) == 0
	result.RejectedInvalid = rejectedInvalid
	return result
}

func looksLikeHashDSMLPseudoToolSyntax(text string) bool {
	lower := strings.ToLower(text)
	return strings.Contains(lower, "<#dsml#") || strings.Contains(lower, "</#dsml#") ||
		strings.Contains(lower, "<#dsm#") || strings.Contains(lower, "</#dsm#")
}

func filterToolCallsDetailed(parsed []ParsedToolCall, availableToolNames ...[]string) ([]ParsedToolCall, []string, bool) {
	out := make([]ParsedToolCall, 0, len(parsed))
	rejectedNames := []string{}
	rejectedInvalid := false
	var allowed map[string]struct{}
	if len(availableToolNames) > 0 && len(availableToolNames[0]) > 0 {
		allowed = toolNameSet(availableToolNames[0])
	}
	for _, tc := range parsed {
		if tc.Name == "" {
			continue
		}
		if tc.Input == nil {
			tc.Input = map[string]any{}
		}
		if hasEmptyRequiredToolParameter(tc) {
			rejectedInvalid = true
			continue
		}
		if hasInvalidJSONValue(tc.Input) {
			rejectedInvalid = true
			continue
		}
		if allowed != nil {
			if _, ok := allowed[strings.ToLower(strings.TrimSpace(tc.Name))]; !ok {
				rejectedNames = append(rejectedNames, tc.Name)
				continue
			}
		}
		out = append(out, tc)
	}
	return out, rejectedNames, rejectedInvalid
}

func toolNameSet(names []string) map[string]struct{} {
	if len(names) == 0 {
		return nil
	}
	out := map[string]struct{}{}
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		out[strings.ToLower(name)] = struct{}{}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func hasInvalidJSONValue(input map[string]any) bool {
	for _, v := range input {
		if _, ok := v.(invalidJSONValue); ok {
			return true
		}
	}
	return false
}

func hasEmptyRequiredToolParameter(tc ParsedToolCall) bool {
	required := requiredToolParameters(tc.Name)
	if len(required) == 0 {
		return false
	}
	for _, name := range required {
		if isBlankToolParameterValue(tc.Input[name]) {
			return true
		}
	}
	return false
}

func requiredToolParameters(name string) []string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "read":
		return []string{"file_path"}
	case "bash", "execute", "execute_command":
		return []string{"command"}
	case "exec_command":
		return []string{"cmd"}
	default:
		return nil
	}
}

func isBlankToolParameterValue(value any) bool {
	if value == nil {
		return true
	}
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v) == ""
	default:
		return false
	}
}

func LooksLikeMalformedRequiredToolCall(text string) bool {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return false
	}
	lower := strings.ToLower(trimmed)
	// DSMDL typo (missing separator between DSML and tag name)
	if strings.Contains(lower, "dsmdl") {
		if strings.Contains(lower, "tool_calls") || strings.Contains(lower, "invoke") || strings.Contains(lower, "parameter") {
			return true
		}
	}
	if looksLikeSentenceInvokeRequiredToolCall(trimmed) {
		return true
	}
	if looksLikeLocalizedPunctuationRequiredToolCall(trimmed) {
		return true
	}
	if looksLikeMalformedSkillCallProtocol(trimmed) {
		return true
	}
	hasDSML, hasCanonical := ContainsToolCallWrapperSyntaxOutsideIgnored(trimmed)
	if !hasDSML && !hasCanonical {
		return looksLikeBareRequiredToolInvoke(trimmed)
	}
	lower = strings.ToLower(trimmed)
	// Official fullwidth DSML that didn't normalize
	if strings.Contains(lower, "<｜dsml｜tool_calls") {
		return true
	}
	requiredPairs := map[string][]string{
		"read":            {"file_path"},
		"bash":            {"command"},
		"execute":         {"command"},
		"execute_command": {"command"},
		"exec_command":    {"cmd"},
	}
	for toolName, params := range requiredPairs {
		if !strings.Contains(lower, toolName) {
			continue
		}
		for _, param := range params {
			if strings.Contains(lower, param) {
				return true
			}
		}
	}
	for searchFrom := 0; searchFrom < len(trimmed); {
		tag, ok := FindToolMarkupTagOutsideIgnored(trimmed, searchFrom)
		if !ok {
			break
		}
		searchFrom = tag.End + 1
		if tag.Closing || tag.Name != "invoke" {
			continue
		}
		attrs := parseXMLTagAttributes(trimmed[tag.NameEnd:tag.End])
		toolName := strings.ToLower(strings.TrimSpace(attrs["name"]))
		if len(requiredPairs[toolName]) > 0 {
			return true
		}
	}
	return false
}

func LooksLikeUnsafeStructuredToolIntent(text string, availableToolNames []string) bool {
	trimmed := strings.TrimSpace(stripFencedCodeBlocks(text))
	if trimmed == "" {
		return false
	}
	lower := strings.ToLower(trimmed)
	if !strings.Contains(lower, "<") || !strings.Contains(lower, ">") {
		return false
	}
	hasStructuredShape := strings.Contains(lower, "</") ||
		strings.Contains(lower, "name=") ||
		strings.Contains(lower, "<![cdata[") ||
		strings.Contains(lower, "=")
	if !hasStructuredShape {
		return false
	}
	if strings.Contains(lower, "dsml") {
		if strings.Contains(lower, "invoke") || strings.Contains(lower, "parameter") {
			return true
		}
		if strings.Contains(lower, "call") && containsAvailableToolName(lower, availableToolNames) {
			return true
		}
	}
	for _, toolName := range availableToolNames {
		toolName = strings.ToLower(strings.TrimSpace(toolName))
		if toolName == "" {
			continue
		}
		if strings.Contains(lower, toolName) && looksLikeStructuredParameterPayload(lower) {
			return true
		}
	}
	for toolName, params := range map[string][]string{
		"read":            {"file_path"},
		"bash":            {"command"},
		"execute":         {"command"},
		"execute_command": {"command"},
		"exec_command":    {"cmd"},
	} {
		if !strings.Contains(lower, toolName) {
			continue
		}
		for _, param := range params {
			if strings.Contains(lower, param) && looksLikeStructuredParameterPayload(lower) {
				return true
			}
		}
	}
	return false
}

func containsAvailableToolName(lower string, availableToolNames []string) bool {
	for _, toolName := range availableToolNames {
		toolName = strings.ToLower(strings.TrimSpace(toolName))
		if toolName != "" && strings.Contains(lower, toolName) {
			return true
		}
	}
	return false
}

func looksLikeStructuredParameterPayload(lower string) bool {
	return strings.Contains(lower, "parameter") ||
		strings.Contains(lower, "param") ||
		strings.Contains(lower, "arg") ||
		strings.Contains(lower, "args") ||
		strings.Contains(lower, "arguments") ||
		strings.Contains(lower, "cdata")
}

func looksLikeMalformedSkillCallProtocol(text string) bool {
	lower := strings.ToLower(text)
	if !strings.Contains(lower, "skill") {
		return false
	}
	if strings.Contains(lower, "<skill>") && strings.Contains(lower, "</skill>") {
		return true
	}
	if strings.Contains(lower, "skill_calls") {
		return true
	}
	return false
}

func looksLikeSentenceInvokeRequiredToolCall(text string) bool {
	lower := strings.ToLower(text)
	hasBeginInvoke := strings.Contains(lower, "begin▁of▁invoke")
	hasBeginSentence := strings.Contains(lower, "begin▁of▁sentence")
	if !hasBeginInvoke && !hasBeginSentence {
		return false
	}
	requiredPairs := map[string][]string{
		"read":            {"file_path"},
		"bash":            {"command"},
		"execute":         {"command"},
		"execute_command": {"command"},
		"exec_command":    {"cmd"},
	}
	if hasBeginSentence {
		sentencePayload := textAfterSentenceStartMarker(lower)
		for toolName := range requiredPairs {
			if startsWithToolName(sentencePayload, toolName) {
				return true
			}
		}
	}
	if !hasBeginInvoke {
		return false
	}
	for toolName, params := range requiredPairs {
		if !strings.Contains(lower, `name="`+toolName+`"`) &&
			!strings.Contains(lower, `name='`+toolName+`'`) &&
			!strings.Contains(lower, `name=“`+toolName+`”`) &&
			!strings.Contains(lower, `name=`+toolName) {
			continue
		}
		for _, param := range params {
			if strings.Contains(lower, param) {
				return true
			}
		}
		return true
	}
	return false
}

func textAfterSentenceStartMarker(lower string) string {
	idx := strings.Index(lower, "begin▁of▁sentence")
	if idx < 0 {
		return ""
	}
	tail := lower[idx+len("begin▁of▁sentence"):]
	if markerEnd := strings.Index(tail, "｜>"); markerEnd >= 0 {
		tail = tail[markerEnd+len("｜>"):]
	}
	return strings.TrimLeft(tail, " \t\r\n")
}

func startsWithToolName(text string, toolName string) bool {
	if !strings.HasPrefix(text, toolName) {
		return false
	}
	if len(text) == len(toolName) {
		return true
	}
	next := text[len(toolName)]
	return (next < 'a' || next > 'z') && (next < '0' || next > '9') && next != '_'
}

func looksLikeLocalizedPunctuationRequiredToolCall(text string) bool {
	lower := strings.ToLower(text)
	hasLocalizedToolTag := strings.Contains(lower, "<｜tool_calls＞") ||
		strings.Contains(lower, "</｜tool_calls＞") ||
		strings.Contains(lower, "<！invoke") ||
		strings.Contains(lower, "</！invoke") ||
		strings.Contains(lower, "<！parameter") ||
		strings.Contains(lower, "</！parameter") ||
		strings.Contains(lower, "</！tool_calls") ||
		strings.Contains(lower, "<！[cdata[")
	if !hasLocalizedToolTag {
		return false
	}
	requiredPairs := map[string][]string{
		"read":            {"file_path"},
		"bash":            {"command"},
		"execute":         {"command"},
		"execute_command": {"command"},
		"exec_command":    {"cmd"},
	}
	for toolName, params := range requiredPairs {
		if !strings.Contains(lower, toolName) {
			continue
		}
		for _, param := range params {
			if strings.Contains(lower, param) {
				return true
			}
		}
	}
	return false
}

func looksLikeToolCallSyntax(text string) bool {
	hasDSML, hasCanonical := ContainsToolCallWrapperSyntaxOutsideIgnored(text)
	return hasDSML || hasCanonical
}

func looksLikeBareRequiredToolInvoke(text string) bool {
	if strings.Contains(strings.ToLower(text), "tool_calls") {
		return false
	}
	tag, ok := FindToolMarkupTagOutsideIgnored(text, 0)
	if !ok || tag.Closing || tag.Name != "invoke" {
		return false
	}
	if strings.TrimSpace(text[:tag.Start]) != "" {
		return false
	}
	if !strings.Contains(strings.ToLower(text[tag.End+1:]), "<parameter") {
		return false
	}
	attrs := parseXMLTagAttributes(text[tag.NameEnd:tag.End])
	return len(requiredToolParameters(attrs["name"])) > 0
}

func stripFencedCodeBlocks(text string) string {
	if text == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(text))

	lines := strings.SplitAfter(text, "\n")
	inFence := false
	fenceMarker := ""
	inCDATA := false
	// Track builder length when a fence opens so we can preserve content
	// collected before the unclosed fence.
	beforeFenceLen := 0
	for _, line := range lines {
		if inCDATA || cdataStartsBeforeFence(line) {
			b.WriteString(line)
			inCDATA = updateCDATAState(inCDATA, line)
			continue
		}
		trimmed := strings.TrimLeft(line, " \t")
		if !inFence {
			if marker, ok := parseFenceOpen(trimmed); ok {
				inFence = true
				fenceMarker = marker
				beforeFenceLen = b.Len()
				continue
			}
			b.WriteString(line)
			continue
		}

		if isFenceClose(trimmed, fenceMarker) {
			inFence = false
			fenceMarker = ""
		}
	}

	if inFence {
		// Unclosed fence: preserve content that was collected before the
		// fence started rather than dropping everything.
		result := b.String()
		if beforeFenceLen > 0 && beforeFenceLen <= len(result) {
			return result[:beforeFenceLen]
		}
		return ""
	}
	return b.String()
}

func cdataStartsBeforeFence(line string) bool {
	cdataIdx := strings.Index(strings.ToLower(line), "<![cdata[")
	if cdataIdx < 0 {
		return false
	}
	fenceIdx := firstFenceMarkerIndex(line)
	return fenceIdx < 0 || cdataIdx < fenceIdx
}

func firstFenceMarkerIndex(line string) int {
	idxBacktick := strings.Index(line, "```")
	idxTilde := strings.Index(line, "~~~")
	switch {
	case idxBacktick < 0:
		return idxTilde
	case idxTilde < 0:
		return idxBacktick
	case idxBacktick < idxTilde:
		return idxBacktick
	default:
		return idxTilde
	}
}

func updateCDATAState(inCDATA bool, line string) bool {
	lower := strings.ToLower(line)
	pos := 0
	state := inCDATA
	for pos < len(lower) {
		if state {
			end := strings.Index(lower[pos:], "]]>")
			if end < 0 {
				return true
			}
			pos += end + len("]]>")
			state = false
			continue
		}
		start := strings.Index(lower[pos:], "<![cdata[")
		if start < 0 {
			return false
		}
		pos += start + len("<![cdata[")
		state = true
	}
	return state
}

func parseFenceOpen(line string) (string, bool) {
	if len(line) < 3 {
		return "", false
	}
	ch := line[0]
	if ch != '`' && ch != '~' {
		return "", false
	}
	count := countLeadingFenceChars(line, ch)
	if count < 3 {
		return "", false
	}
	return strings.Repeat(string(ch), count), true
}

func isFenceClose(line, marker string) bool {
	if marker == "" {
		return false
	}
	ch := marker[0]
	if line == "" || line[0] != ch {
		return false
	}
	count := countLeadingFenceChars(line, ch)
	if count < len(marker) {
		return false
	}
	rest := strings.TrimSpace(line[count:])
	return rest == ""
}

func countLeadingFenceChars(line string, ch byte) int {
	count := 0
	for count < len(line) && line[count] == ch {
		count++
	}
	return count
}
