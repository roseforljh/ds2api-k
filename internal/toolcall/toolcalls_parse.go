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
	return parseToolCallsDetailedXMLOnly(text)
}

func ParseStandaloneToolCalls(text string, availableToolNames []string) []ParsedToolCall {
	return ParseStandaloneToolCallsDetailed(text, availableToolNames).Calls
}

func ParseStandaloneToolCallsDetailed(text string, availableToolNames []string) ToolCallParseResult {
	return parseToolCallsDetailedXMLOnly(text)
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

func parseToolCallsDetailedXMLOnly(text string) ToolCallParseResult {
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

	normalized, ok := normalizeDSMLToolCallMarkup(trimmed)
	if !ok {
		if result.SawToolCallSyntax && LooksLikeMalformedRequiredToolCall(trimmed) {
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
		if result.SawToolCallSyntax && LooksLikeMalformedRequiredToolCall(trimmed) {
			result.RejectedInvalid = true
		}
		return result
	}

	result.SawToolCallSyntax = true
	calls, rejectedNames, rejectedInvalid := filterToolCallsDetailed(parsed)
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

func filterToolCallsDetailed(parsed []ParsedToolCall) ([]ParsedToolCall, []string, bool) {
	out := make([]ParsedToolCall, 0, len(parsed))
	rejectedInvalid := false
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
		out = append(out, tc)
	}
	return out, nil, rejectedInvalid
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
	hasDSML, hasCanonical := ContainsToolCallWrapperSyntaxOutsideIgnored(trimmed)
	if !hasDSML && !hasCanonical {
		return false
	}
	lower := strings.ToLower(trimmed)
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
