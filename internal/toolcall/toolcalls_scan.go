package toolcall

import "strings"

var toolMarkupNames = []string{"tool_calls", "invoke", "parameter"}

type ToolMarkupTag struct {
	Start       int
	End         int
	NameStart   int
	NameEnd     int
	Name        string
	Closing     bool
	SelfClosing bool
	DSMLLike    bool
	Canonical   bool
}

func ContainsToolMarkupSyntaxOutsideIgnored(text string) (hasDSML, hasCanonical bool) {
	lower := strings.ToLower(text)
	for i := 0; i < len(text); {
		next, advanced, blocked := skipXMLIgnoredSection(lower, i)
		if blocked {
			return hasDSML, hasCanonical
		}
		if advanced {
			i = next
			continue
		}
		if tag, ok := scanToolMarkupTagAt(text, i); ok {
			if tag.DSMLLike {
				hasDSML = true
			} else {
				hasCanonical = true
			}
			if hasDSML && hasCanonical {
				return true, true
			}
			i = tag.End + 1
			continue
		}
		i++
	}
	return hasDSML, hasCanonical
}

func ContainsToolCallWrapperSyntaxOutsideIgnored(text string) (hasDSML, hasCanonical bool) {
	lower := strings.ToLower(text)
	for i := 0; i < len(text); {
		next, advanced, blocked := skipXMLIgnoredSection(lower, i)
		if blocked {
			return hasDSML, hasCanonical
		}
		if advanced {
			i = next
			continue
		}
		if tag, ok := scanToolMarkupTagAt(text, i); ok {
			if tag.Name != "tool_calls" {
				i = tag.End + 1
				continue
			}
			if tag.DSMLLike {
				hasDSML = true
			} else {
				hasCanonical = true
			}
			if hasDSML && hasCanonical {
				return true, true
			}
			i = tag.End + 1
			continue
		}
		i++
	}
	return hasDSML, hasCanonical
}

func FindToolMarkupTagOutsideIgnored(text string, start int) (ToolMarkupTag, bool) {
	lower := strings.ToLower(text)
	for i := maxInt(start, 0); i < len(text); {
		next, advanced, blocked := skipXMLIgnoredSection(lower, i)
		if blocked {
			return ToolMarkupTag{}, false
		}
		if advanced {
			i = next
			continue
		}
		if tag, ok := scanToolMarkupTagAt(text, i); ok {
			return tag, true
		}
		i++
	}
	return ToolMarkupTag{}, false
}

func FindMatchingToolMarkupClose(text string, open ToolMarkupTag) (ToolMarkupTag, bool) {
	if text == "" || open.Name == "" || open.Closing {
		return ToolMarkupTag{}, false
	}
	depth := 1
	for pos := open.End + 1; pos < len(text); {
		tag, ok := FindToolMarkupTagOutsideIgnored(text, pos)
		if !ok {
			return ToolMarkupTag{}, false
		}
		if tag.Name != open.Name {
			pos = tag.End + 1
			continue
		}
		if tag.Closing {
			depth--
			if depth == 0 {
				return tag, true
			}
		} else if !tag.SelfClosing {
			depth++
		}
		pos = tag.End + 1
	}
	return ToolMarkupTag{}, false
}

func scanToolMarkupTagAt(text string, start int) (ToolMarkupTag, bool) {
	if start < 0 || start >= len(text) || text[start] != '<' {
		return ToolMarkupTag{}, false
	}
	lower := strings.ToLower(text)
	i := start + 1
	closing := false
	if i < len(text) && text[i] == '/' {
		closing = true
		i++
	}
	dsmlLike := false
	if next, ok := consumeToolMarkupPipe(text, i); ok {
		dsmlLike = true
		i = next
	}
	if next, markerClosing, ok := consumeBracketToolMarkupAlias(text, i); ok {
		dsmlLike = true
		closing = closing || markerClosing
		i = next
	} else if next, markerClosing, ok := consumeHashToolMarkupAlias(text, i); ok {
		dsmlLike = true
		closing = closing || markerClosing
		i = next
	} else if strings.HasPrefix(lower[i:], "dsml") {
		dsmlLike = true
		i += len("dsml")
		for next, ok := consumeToolMarkupSeparator(text, i); ok; next, ok = consumeToolMarkupSeparator(text, i) {
			i = next
		}
		if strings.HasPrefix(lower[i:], "dsep") {
			i += len("dsep")
			for next, ok := consumeToolMarkupSeparator(text, i); ok; next, ok = consumeToolMarkupSeparator(text, i) {
				i = next
			}
		}
	} else if strings.HasPrefix(lower[i:], "dsm") {
		dsmlLike = true
		i += len("dsm")
		for next, ok := consumeToolMarkupSeparator(text, i); ok; next, ok = consumeToolMarkupSeparator(text, i) {
			i = next
		}
	}
	name, nameLen := matchToolMarkupName(lower, i)
	if nameLen == 0 {
		return ToolMarkupTag{}, false
	}
	nameEnd := i + nameLen
	if !hasToolMarkupBoundary(text, nameEnd) {
		return ToolMarkupTag{}, false
	}
	end := findXMLTagEnd(text, nameEnd)
	if end < 0 {
		return ToolMarkupTag{}, false
	}
	trimmed := strings.TrimSpace(text[start : end+1])
	return ToolMarkupTag{
		Start:       start,
		End:         end,
		NameStart:   i,
		NameEnd:     nameEnd,
		Name:        name,
		Closing:     closing,
		SelfClosing: strings.HasSuffix(trimmed, "/>"),
		DSMLLike:    dsmlLike,
		Canonical:   !dsmlLike,
	}, true
}

func matchToolMarkupName(lower string, start int) (string, int) {
	for _, name := range toolMarkupNames {
		if strings.HasPrefix(lower[start:], name) {
			return name, len(name)
		}
	}
	return "", 0
}

func consumeToolMarkupPipe(text string, idx int) (int, bool) {
	if idx >= len(text) {
		return idx, false
	}
	start := idx
	for idx < len(text) {
		if text[idx] == '|' {
			idx++
			continue
		}
		if strings.HasPrefix(text[idx:], "｜") {
			idx += len("｜")
			continue
		}
		break
	}
	return idx, idx > start
}

func consumeBracketToolMarkupAlias(text string, idx int) (next int, closing bool, ok bool) {
	if idx >= len(text) || !strings.HasPrefix(text[idx:], "⌜") {
		return idx, false, false
	}
	endRel := strings.Index(text[idx+len("⌜"):], "⌝")
	if endRel < 0 {
		return idx, false, false
	}
	bodyStart := idx + len("⌜")
	bodyEnd := bodyStart + endRel
	marker := strings.TrimSpace(strings.ToLower(text[bodyStart:bodyEnd]))
	if strings.HasPrefix(marker, "/") {
		closing = true
		marker = strings.TrimSpace(strings.TrimPrefix(marker, "/"))
	}
	if marker != "dsml" && marker != "dsm" {
		return idx, false, false
	}
	return bodyEnd + len("⌝"), closing, true
}

func consumeHashToolMarkupAlias(text string, idx int) (next int, closing bool, ok bool) {
	if idx >= len(text) || text[idx] != '#' {
		return idx, false, false
	}
	endRel := strings.Index(text[idx+1:], "#")
	if endRel < 0 {
		return idx, false, false
	}
	bodyStart := idx + 1
	bodyEnd := bodyStart + endRel
	marker := strings.TrimSpace(strings.ToLower(text[bodyStart:bodyEnd]))
	if strings.HasPrefix(marker, "/") {
		closing = true
		marker = strings.TrimSpace(strings.TrimPrefix(marker, "/"))
	}
	if marker != "dsml" && marker != "dsm" {
		return idx, false, false
	}
	return bodyEnd + 1, closing, true
}

func consumeToolMarkupSeparator(text string, idx int) (int, bool) {
	if idx >= len(text) {
		return idx, false
	}
	if text[idx] == '_' || text[idx] == ' ' || text[idx] == '\t' || text[idx] == '\r' || text[idx] == '\n' {
		return idx + 1, true
	}
	if next, ok := consumeToolMarkupPipe(text, idx); ok {
		return next, true
	}
	return idx, false
}

func hasToolMarkupBoundary(text string, idx int) bool {
	if idx >= len(text) {
		return true
	}
	switch text[idx] {
	case ' ', '\t', '\n', '\r', '>', '/':
		return true
	default:
		return false
	}
}
