package toolstream

import (
	"strings"

	"ds2api/internal/toolcall"
)

const (
	smlSentinelOpen  = "<sml_dollar_em_ollar_"
	smlSentinelClose = "</sml_dollar_em_ollar_"
)

func suppressDSMDLToolLikeFragment(state *State, content string) bool {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return false
	}
	lower := strings.ToLower(trimmed)
	if !strings.Contains(lower, "dsmdl") {
		return false
	}
	if strings.Contains(lower, "tool_calls") || strings.Contains(lower, "invoke") || strings.Contains(lower, "parameter") {
		state.MalformedToolFeedback = trimmed
		return true
	}
	return false
}

func ProcessChunk(state *State, chunk string, toolNames []string) []Event {
	if state == nil {
		return nil
	}
	if chunk != "" {
		state.pending.WriteString(chunk)
	}
	events := make([]Event, 0, 2)
	if len(state.pendingToolCalls) > 0 {
		events = append(events, Event{ToolCalls: state.pendingToolCalls})
		state.pendingToolRaw = ""
		state.pendingToolCalls = nil
	}

	for {
		if state.capturing {
			if state.pending.Len() > 0 {
				state.capture.WriteString(state.pending.String())
				state.pending.Reset()
			}
			prefix, calls, suffix, ready := consumeToolCapture(state, toolNames)
			if !ready {
				break
			}
			captured := state.capture.String()
			malformed := (toolcall.LooksLikeMalformedRequiredToolCall(captured) || toolcall.LooksLikeUnsafeStructuredToolIntent(captured, toolNames)) && len(calls) == 0
			if malformed {
				state.MalformedToolFeedback = captured
				prefix = ""
				suffix = ""
			}
			if strings.TrimSpace(state.MalformedToolFeedback) != "" {
				prefix = ""
				suffix = ""
			}
			state.capture.Reset()
			state.capturing = false
			state.resetIncrementalToolState()
			if len(calls) > 0 {
				if prefix != "" {
					if !suppressDSMDLToolLikeFragment(state, prefix) {
						state.noteText(prefix)
						events = append(events, Event{Content: prefix})
					}
				}
				_ = captured
				events = append(events, Event{ToolCalls: calls})
				if suffix != "" {
					state.pending.WriteString(suffix)
				}
				continue
			}
			if prefix != "" {
				if !suppressDSMDLToolLikeFragment(state, prefix) {
					state.noteText(prefix)
					events = append(events, Event{Content: prefix})
				}
			}
			if suffix != "" {
				state.pending.WriteString(suffix)
			}
			continue
		}

		pending := state.pending.String()
		if pending == "" {
			break
		}
		start := findToolSegmentStart(state, pending, toolNames)
		if start >= 0 {
			prefix := pending[:start]
			if shouldAttachPrefixToLocalizedMalformedCapture(prefix, pending[start:]) {
				start = 0
				prefix = ""
			}
			if prefix != "" {
				state.noteText(prefix)
				events = append(events, Event{Content: prefix})
			}
			state.pending.Reset()
			state.capture.WriteString(pending[start:])
			state.capturing = true
			state.resetIncrementalToolState()
			continue
		}

		safe, hold := splitSafeContentForToolDetection(state, pending)
		if safe == "" {
			break
		}
		state.pending.Reset()
		state.pending.WriteString(hold)
		if suppressDSMDLToolLikeFragment(state, safe) {
			continue
		}
		state.noteText(safe)
		events = append(events, Event{Content: safe})
	}

	return events
}

func Flush(state *State, toolNames []string) []Event {
	if state == nil {
		return nil
	}
	events := ProcessChunk(state, "", toolNames)
	if len(state.pendingToolCalls) > 0 {
		events = append(events, Event{ToolCalls: state.pendingToolCalls})
		state.pendingToolRaw = ""
		state.pendingToolCalls = nil
	}
	if state.capturing {
		consumedPrefix, consumedCalls, consumedSuffix, ready := consumeToolCapture(state, toolNames)
		if ready {
			captured := state.capture.String()
			if len(consumedCalls) == 0 && (toolcall.LooksLikeMalformedRequiredToolCall(captured) || toolcall.LooksLikeUnsafeStructuredToolIntent(captured, toolNames)) {
				state.MalformedToolFeedback = captured
				consumedPrefix = ""
				consumedSuffix = ""
			}
			if strings.TrimSpace(state.MalformedToolFeedback) != "" {
				consumedPrefix = ""
				consumedSuffix = ""
			}
			if consumedPrefix != "" {
				if !suppressDSMDLToolLikeFragment(state, consumedPrefix) {
					state.noteText(consumedPrefix)
					events = append(events, Event{Content: consumedPrefix})
				}
			}
			if len(consumedCalls) > 0 {
				events = append(events, Event{ToolCalls: consumedCalls})
			}
			if consumedSuffix != "" {
				if !suppressDSMDLToolLikeFragment(state, consumedSuffix) {
					state.noteText(consumedSuffix)
					events = append(events, Event{Content: consumedSuffix})
				}
			}
		} else {
			content := state.capture.String()
			if content != "" {
				if looksLikeSMLSentinelToolProtocol(content) {
					state.capture.Reset()
					state.capturing = false
					state.resetIncrementalToolState()
					if state.pending.Len() > 0 {
						content := state.pending.String()
						state.noteText(content)
						events = append(events, Event{Content: content})
						state.pending.Reset()
					}
					return events
				}
				if toolcall.LooksLikeMalformedRequiredToolCall(content) {
					state.MalformedToolFeedback = content
					state.capture.Reset()
					state.capturing = false
					state.resetIncrementalToolState()
					state.pending.Reset()
					return events
				}
				recovered := toolcall.SanitizeLooseCDATA(content)
				if recovered != content {
					if prefix, calls, suffix, recoveredReady := consumeXMLToolCapture(recovered, toolNames); recoveredReady && len(calls) > 0 {
						if prefix != "" {
							if !suppressDSMDLToolLikeFragment(state, prefix) {
								state.noteText(prefix)
								events = append(events, Event{Content: prefix})
							}
						}
						events = append(events, Event{ToolCalls: calls})
						if suffix != "" {
							if !suppressDSMDLToolLikeFragment(state, suffix) {
								state.noteText(suffix)
								events = append(events, Event{Content: suffix})
							}
						}
					} else {
						if toolcall.LooksLikeMalformedRequiredToolCall(content) {
							state.MalformedToolFeedback = content
						} else {
							// If capture never resolved into a real tool call, release
							// the buffered text instead of swallowing it.
							state.noteText(content)
							events = append(events, Event{Content: content})
						}
					}
				} else {
					if toolcall.LooksLikeMalformedRequiredToolCall(content) {
						state.MalformedToolFeedback = content
					} else {
						// If capture never resolved into a real tool call, release the
						// buffered text instead of swallowing it.
						state.noteText(content)
						events = append(events, Event{Content: content})
					}
				}
			}
		}
		state.capture.Reset()
		state.capturing = false
		state.resetIncrementalToolState()
	}
	if state.pending.Len() > 0 {
		content := state.pending.String()
		if toolcall.LooksLikeMalformedRequiredToolCall(content) || toolcall.LooksLikeUnsafeStructuredToolIntent(content, toolNames) {
			state.MalformedToolFeedback = content
		} else {
			// If pending never resolved into a real tool call, release it as text.
			if !suppressDSMDLToolLikeFragment(state, content) {
				state.noteText(content)
				events = append(events, Event{Content: content})
			}
		}
		state.pending.Reset()
	}
	return events
}

func splitSafeContentForToolDetection(state *State, s string) (safe, hold string) {
	if s == "" {
		return "", ""
	}
	if smlIdx := findPartialSMLSentinelStart(s); smlIdx >= 0 {
		if insideCodeFenceWithState(state, s[:smlIdx]) {
			return s, ""
		}
		if smlIdx > 0 {
			return s[:smlIdx], s[smlIdx:]
		}
		return "", s
	}
	if xmlIdx := findPartialXMLToolTagStart(s); xmlIdx >= 0 {
		if insideCodeFenceWithState(state, s[:xmlIdx]) {
			return s, ""
		}
		if xmlIdx > 0 {
			return s[:xmlIdx], s[xmlIdx:]
		}
		return "", s
	}
	return s, ""
}

func shouldAttachPrefixToLocalizedMalformedCapture(prefix string, segment string) bool {
	trimmedPrefix := strings.TrimSpace(prefix)
	if trimmedPrefix == "" {
		return false
	}
	if strings.Contains(strings.ToLower(segment), "<invoke") || strings.Contains(strings.ToLower(segment), "<parameter") {
		return true
	}
	if trimmedPrefix != "●" && trimmedPrefix != "•" && trimmedPrefix != "-" && !strings.EqualFold(trimmedPrefix, "Skill") {
		return false
	}
	lowerSegment := strings.ToLower(segment)
	return strings.Contains(lowerSegment, "<｜tool_calls＞") ||
		strings.Contains(lowerSegment, "<！invoke") ||
		strings.Contains(lowerSegment, "<！parameter") ||
		strings.Contains(lowerSegment, "begin▁of▁sentence") ||
		strings.Contains(lowerSegment, "begin▁of▁invoke") ||
		strings.Contains(lowerSegment, "<skill>") ||
		strings.Contains(lowerSegment, "skill_calls")
}

func findToolSegmentStart(state *State, s string, toolNames []string) int {
	if s == "" {
		return -1
	}
	for searchFrom := 0; searchFrom < len(s); {
		idx := strings.Index(s[searchFrom:], "<")
		if idx < 0 {
			break
		}
		idx += searchFrom
		tagEnd := strings.Index(s[idx:], ">")
		if tagEnd >= 0 {
			tagText := strings.ToLower(s[idx : idx+tagEnd+1])
			recognized := false
			if tag, ok := toolcall.FindToolMarkupTagOutsideIgnored(s, idx); ok && tag.Start == idx {
				recognized = true
			}
			if !strings.HasPrefix(tagText, "</") && !recognized && strings.Contains(tagText, "dsml") && strings.Contains(tagText, "call") && !insideCodeFenceWithState(state, s[:idx]) && toolcall.LooksLikeUnsafeStructuredToolIntent(s[idx:], toolNames) {
				return idx
			}
		}
		searchFrom = idx + 1
	}
	for searchFrom := 0; searchFrom < len(s); {
		tag, ok := toolcall.FindToolMarkupTagOutsideIgnored(s, searchFrom)
		if !ok {
			break
		}
		if !tag.Closing && !insideCodeFenceWithState(state, s[:tag.Start]) {
			return tag.Start
		}
		searchFrom = tag.End + 1
	}
	for searchFrom := 0; searchFrom < len(s); {
		idx := strings.Index(s[searchFrom:], "<")
		if idx < 0 {
			break
		}
		idx += searchFrom
		if !insideCodeFenceWithState(state, s[:idx]) && toolcall.LooksLikeUnsafeStructuredToolIntent(s[idx:], toolNames) {
			return idx
		}
		searchFrom = idx + 1
	}
	lower := strings.ToLower(s)
	offset := 0
	for {
		bestKeyIdx := -1
		matchedTag := ""
		for _, tag := range xmlToolTagsToDetect {
			idx := strings.Index(lower[offset:], tag)
			if idx >= 0 {
				idx += offset
				if bestKeyIdx < 0 || idx < bestKeyIdx {
					bestKeyIdx = idx
					matchedTag = tag
				}
			}
		}
		if idx := strings.Index(lower[offset:], smlSentinelOpen); idx >= 0 {
			idx += offset
			if bestKeyIdx < 0 || idx < bestKeyIdx {
				bestKeyIdx = idx
				matchedTag = smlSentinelOpen
			}
		}
		if bestKeyIdx < 0 {
			return -1
		}
		if !insideCodeFenceWithState(state, s[:bestKeyIdx]) {
			return bestKeyIdx
		}
		offset = bestKeyIdx + len(matchedTag)
	}
}

func consumeToolCapture(state *State, toolNames []string) (prefix string, calls []toolcall.ParsedToolCall, suffix string, ready bool) {
	captured := state.capture.String()
	if captured == "" {
		return "", nil, "", false
	}

	if prefix, suffix, ready := consumeSMLSentinelCapture(captured); ready {
		return prefix, nil, suffix, true
	}
	if looksLikeSMLSentinelToolProtocol(captured) {
		return "", nil, "", false
	}

	// XML tool call extraction only.
	if xmlPrefix, xmlCalls, xmlSuffix, xmlReady := consumeXMLToolCapture(captured, toolNames); xmlReady {
		return xmlPrefix, xmlCalls, xmlSuffix, true
	}
	// If XML tags are present but block is incomplete, keep buffering.
	if hasOpenXMLToolTag(captured) {
		return "", nil, "", false
	}
	if hasOpenLocalizedToolTag(captured) {
		return "", nil, "", false
	}
	if hasOpenSentenceInvokeToolTag(captured) {
		return "", nil, "", false
	}
	if shouldKeepBareInvokeCapture(captured) {
		return "", nil, "", false
	}
	return captured, nil, "", true
}

func hasOpenLocalizedToolTag(captured string) bool {
	lower := strings.ToLower(captured)
	if !strings.Contains(lower, "<｜tool_calls＞") && !strings.Contains(lower, "<！invoke") && !strings.Contains(lower, "<！parameter") {
		return false
	}
	if strings.Contains(lower, "</！tool_calls＞") || strings.Contains(lower, "</｜tool_calls＞") {
		return false
	}
	return true
}

func hasOpenSentenceInvokeToolTag(captured string) bool {
	lower := strings.ToLower(captured)
	if !strings.Contains(lower, "begin▁of▁sentence") && !strings.Contains(lower, "begin▁of▁invoke") {
		return false
	}
	return !strings.Contains(lower, "end▁of▁sentence")
}

func findPartialSMLSentinelStart(s string) int {
	lastLT := strings.LastIndex(s, "<")
	if lastLT < 0 {
		return -1
	}
	tail := strings.ToLower(s[lastLT:])
	if strings.Contains(tail, ">") {
		return -1
	}
	if strings.HasPrefix(smlSentinelOpen, tail) || strings.HasPrefix(smlSentinelClose, tail) {
		return lastLT
	}
	return -1
}

func looksLikeSMLSentinelToolProtocol(s string) bool {
	return strings.Contains(strings.ToLower(s), smlSentinelOpen)
}

func consumeSMLSentinelCapture(captured string) (prefix, suffix string, ready bool) {
	lower := strings.ToLower(captured)
	open := strings.Index(lower, smlSentinelOpen)
	if open < 0 {
		return "", "", false
	}
	close := strings.Index(lower[open+len(smlSentinelOpen):], smlSentinelClose)
	if close < 0 {
		return "", "", false
	}
	close += open + len(smlSentinelOpen)
	return captured[:open], captured[close+len(smlSentinelClose):], true
}
