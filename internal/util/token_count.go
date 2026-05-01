package util

import (
	"strings"
	"sync"

	tiktoken "github.com/hupe1980/go-tiktoken"
)

const defaultTokenizerModel = "gpt-4o"

var tokenizerPools sync.Map

func CountPromptTokens(text, model string) int {
	base := maxTokenCount(EstimateTokens(text), countWithTokenizer(text, model))
	if base <= 0 {
		return 0
	}
	return base + conservativePromptPadding(base)
}

func CountOutputTokens(text, model string) int {
	base := maxTokenCount(EstimateTokens(text), countWithTokenizer(text, model))
	if base <= 0 {
		return 0
	}
	return base
}

func countWithTokenizer(text, model string) int {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0
	}
	model = tokenizerModelForCount(model)
	encoding, err := borrowTokenizer(model)
	if err != nil || encoding == nil {
		return 0
	}
	defer returnTokenizer(model, encoding)
	ids, _, err := encoding.Encode(text, nil, nil)
	if err != nil {
		return 0
	}
	return len(ids)
}

func tokenizerModelForCount(model string) string {
	model = strings.ToLower(strings.TrimSpace(model))
	switch {
	case strings.HasPrefix(model, "claude"):
		return "claude"
	default:
		return defaultTokenizerModel
	}
}

func borrowTokenizer(model string) (*tiktoken.Encoding, error) {
	poolAny, _ := tokenizerPools.LoadOrStore(model, &sync.Pool{
		New: func() any {
			encoding, err := tiktoken.NewEncodingForModel(model)
			if err != nil {
				return err
			}
			return encoding
		},
	})
	v := poolAny.(*sync.Pool).Get()
	switch x := v.(type) {
	case *tiktoken.Encoding:
		return x, nil
	case error:
		return nil, x
	default:
		return nil, nil
	}
}

func returnTokenizer(model string, encoding *tiktoken.Encoding) {
	if encoding == nil {
		return
	}
	if poolAny, ok := tokenizerPools.Load(model); ok {
		poolAny.(*sync.Pool).Put(encoding)
	}
}

func conservativePromptPadding(base int) int {
	padding := base / 50
	if padding < 4 {
		padding = 4
	}
	return padding
}

func maxTokenCount(values ...int) int {
	best := 0
	for _, v := range values {
		if v > best {
			best = v
		}
	}
	return best
}
