package shared

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var citationMarkerPattern = regexp.MustCompile(`(?i)\[(citation|reference):\s*(\d+)\]`)

func ReplaceCitationMarkersWithLinks(text string, links map[int]string) string {
	if strings.TrimSpace(text) == "" || len(links) == 0 {
		return text
	}
	zeroBased := strings.Contains(strings.ToLower(text), "[reference:0]")
	return citationMarkerPattern.ReplaceAllStringFunc(text, func(match string) string {
		sub := citationMarkerPattern.FindStringSubmatch(match)
		if len(sub) < 3 {
			return match
		}
		idx, err := strconv.Atoi(strings.TrimSpace(sub[2]))
		if err != nil || idx < 0 {
			return match
		}
		lookupIdx := idx
		if zeroBased {
			lookupIdx = idx + 1
		}
		url := strings.TrimSpace(links[lookupIdx])
		if url == "" {
			return match
		}
		return fmt.Sprintf("[%d](%s)", idx, url)
	})
}
