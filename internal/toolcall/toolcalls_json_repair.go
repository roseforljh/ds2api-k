package toolcall

import (
	"regexp"
	"strings"
)

func repairInvalidJSONBackslashes(s string) string {
	if !strings.Contains(s, "\\") {
		return s
	}
	var out strings.Builder
	out.Grow(len(s) + 10)
	runes := []rune(s)
	for i := 0; i < len(runes); i++ {
		if runes[i] == '\\' {
			if i+1 < len(runes) {
				next := runes[i+1]
				switch next {
				case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
					out.WriteRune('\\')
					out.WriteRune(next)
					i++
					continue
				case 'u':
					if i+5 < len(runes) {
						isHex := true
						for j := 1; j <= 4; j++ {
							r := runes[i+1+j]
							if (r < '0' || r > '9') && (r < 'a' || r > 'f') && (r < 'A' || r > 'F') {
								isHex = false
								break
							}
						}
						if isHex {
							out.WriteRune('\\')
							out.WriteRune('u')
							for j := 1; j <= 4; j++ {
								out.WriteRune(runes[i+1+j])
							}
							i += 5
							continue
						}
					}
				}
			}
			// Not a valid escape sequence, double it
			out.WriteString("\\\\")
		} else {
			out.WriteRune(runes[i])
		}
	}
	return out.String()
}

var unquotedKeyPattern = regexp.MustCompile(`([{,]\s*)([a-zA-Z_][a-zA-Z0-9_]*)\s*:`)

// missingArrayBracketsPattern identifies a sequence of two or more JSON objects separated by commas
// that immediately follow a colon, which indicates a missing array bracket `[` `]`.
// E.g., "key": {"a": 1}, {"b": 2} -> "key": [{"a": 1}, {"b": 2}]
// NOTE: The pattern uses (?:[^{}]|\{[^{}]*\})* to support single-level nested {} objects,
// which handles cases like {"content": "x", "input": {"q": "y"}}
var missingArrayBracketsPattern = regexp.MustCompile(`(:\s*)(\{(?:[^{}]|\{[^{}]*\})*\}(?:\s*,\s*\{(?:[^{}]|\{[^{}]*\})*\})+)`)

func RepairLooseJSON(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return s
	}
	// 1. Replace unquoted keys: {key: -> {"key":
	s = unquotedKeyPattern.ReplaceAllString(s, `$1"$2":`)

	// 2. Heuristic: Fix missing array brackets for list of objects
	// e.g., : {obj1}, {obj2} -> : [{obj1}, {obj2}]
	// This specifically addresses DeepSeek's "list hallucination"
	repaired := repairMissingArrayBrackets(s)
	if repaired != s {
		return repaired
	}
	s = missingArrayBracketsPattern.ReplaceAllString(s, `$1[$2]`)

	return s
}

func repairMissingArrayBrackets(s string) string {
	var out strings.Builder
	changed := false
	copyFrom := 0
	for i := 0; i < len(s); i++ {
		if s[i] != ':' || !isOutsideJSONString(s, i) {
			continue
		}
		valueStart := skipJSONSpaces(s, i+1)
		if valueStart >= len(s) || s[valueStart] != '{' {
			continue
		}
		firstEnd := parseJSONCompositeEnd(s, valueStart)
		if firstEnd < 0 {
			continue
		}
		next := skipJSONSpaces(s, firstEnd+1)
		if next >= len(s) || s[next] != ',' {
			continue
		}
		secondStart := skipJSONSpaces(s, next+1)
		if secondStart >= len(s) || s[secondStart] != '{' {
			continue
		}
		end := firstEnd
		count := 1
		for {
			comma := skipJSONSpaces(s, end+1)
			if comma >= len(s) || s[comma] != ',' {
				break
			}
			objStart := skipJSONSpaces(s, comma+1)
			if objStart >= len(s) || s[objStart] != '{' {
				break
			}
			objEnd := parseJSONCompositeEnd(s, objStart)
			if objEnd < 0 {
				break
			}
			end = objEnd
			count++
		}
		if count < 2 {
			continue
		}
		out.WriteString(s[copyFrom:valueStart])
		out.WriteByte('[')
		out.WriteString(s[valueStart : end+1])
		out.WriteByte(']')
		copyFrom = end + 1
		i = end
		changed = true
	}
	if !changed {
		return s
	}
	out.WriteString(s[copyFrom:])
	return out.String()
}

func parseJSONCompositeEnd(s string, start int) int {
	if start >= len(s) || (s[start] != '{' && s[start] != '[') {
		return -1
	}
	stack := []byte{s[start]}
	inString := false
	escaped := false
	for i := start + 1; i < len(s); i++ {
		ch := s[i]
		if inString {
			if escaped {
				escaped = false
				continue
			}
			if ch == '\\' {
				escaped = true
				continue
			}
			if ch == '"' {
				inString = false
			}
			continue
		}
		switch ch {
		case '"':
			inString = true
		case '{', '[':
			stack = append(stack, ch)
		case '}', ']':
			if len(stack) == 0 {
				return -1
			}
			open := stack[len(stack)-1]
			if (open == '{' && ch != '}') || (open == '[' && ch != ']') {
				return -1
			}
			stack = stack[:len(stack)-1]
			if len(stack) == 0 {
				return i
			}
		}
	}
	return -1
}

func skipJSONSpaces(s string, idx int) int {
	for idx < len(s) {
		switch s[idx] {
		case ' ', '\t', '\n', '\r':
			idx++
		default:
			return idx
		}
	}
	return idx
}

func isOutsideJSONString(s string, idx int) bool {
	inString := false
	escaped := false
	for i := 0; i < idx && i < len(s); i++ {
		ch := s[i]
		if escaped {
			escaped = false
			continue
		}
		if ch == '\\' {
			escaped = inString
			continue
		}
		if ch == '"' {
			inString = !inString
		}
	}
	return !inString
}

func parseLooseJSONArrayValue(raw, paramName string) ([]any, bool) {
	if preservesCDATAStringParameter(paramName) {
		return nil, false
	}
	segments, ok := splitTopLevelJSONValues(htmlUnescapeForLooseArray(raw))
	if !ok {
		return nil, false
	}
	out := make([]any, 0, len(segments))
	for _, segment := range segments {
		if parsed, ok := parseJSONLiteralValue(segment); ok {
			out = append(out, parsed)
			continue
		}
		repaired := RepairLooseJSON(segment)
		if repaired != segment {
			if parsed, ok := parseJSONLiteralValue(repaired); ok {
				out = append(out, parsed)
				continue
			}
		}
		return nil, false
	}
	return out, true
}

func htmlUnescapeForLooseArray(raw string) string {
	raw = strings.TrimSpace(raw)
	raw = strings.ReplaceAll(raw, "&quot;", `"`)
	raw = strings.ReplaceAll(raw, "&#34;", `"`)
	raw = strings.ReplaceAll(raw, "&amp;", "&")
	raw = strings.ReplaceAll(raw, "&lt;", "<")
	raw = strings.ReplaceAll(raw, "&gt;", ">")
	return raw
}

func splitTopLevelJSONValues(raw string) ([]string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, false
	}
	values := make([]string, 0, 2)
	start := 0
	depth := 0
	inString := false
	escaped := false
	for i, r := range raw {
		if inString {
			if escaped {
				escaped = false
				continue
			}
			switch r {
			case '\\':
				escaped = true
			case '"':
				inString = false
			}
			continue
		}
		switch r {
		case '"':
			inString = true
		case '{', '[':
			depth++
		case '}', ']':
			if depth > 0 {
				depth--
			}
		case ',':
			if depth == 0 {
				segment := strings.TrimSpace(raw[start:i])
				if segment == "" {
					return nil, false
				}
				values = append(values, segment)
				start = i + len(string(r))
			}
		}
	}
	last := strings.TrimSpace(raw[start:])
	if last == "" {
		return nil, false
	}
	values = append(values, last)
	if len(values) < 2 {
		return nil, false
	}
	return values, true
}
