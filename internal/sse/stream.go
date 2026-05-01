package sse

import (
	"bufio"
	"context"
	"io"
	"time"
)

const (
	parsedLineBufferSize = 128
	lineReaderBufferSize = 64 * 1024
	minFlushChars        = 160
	maxFlushWait         = 80 * time.Millisecond
)

// StartParsedLinePump scans an upstream DeepSeek SSE body and emits normalized
// line parse results. It centralizes scanner setup + current fragment type
// tracking for all streaming adapters. Small content chunks are batched
// (minFlushChars / maxFlushWait) to avoid overwhelming downstream clients.
func StartParsedLinePump(ctx context.Context, body io.Reader, thinkingEnabled bool, initialType string) (<-chan LineResult, <-chan error) {
	out := make(chan LineResult, parsedLineBufferSize)
	done := make(chan error, 1)
	go func() {
		defer close(out)
		type scanItem struct {
			line []byte
			err  error
			eof  bool
		}
		lineCh := make(chan scanItem, 1)
		stopReader := make(chan struct{})
		defer close(stopReader)
		go func() {
			sendScanItem := func(item scanItem) bool {
				select {
				case lineCh <- item:
					return true
				case <-ctx.Done():
					return false
				case <-stopReader:
					return false
				}
			}
			defer close(lineCh)
			reader := bufio.NewReaderSize(body, lineReaderBufferSize)
			for {
				line, err := reader.ReadBytes('\n')
				if len(line) > 0 {
					line = append([]byte{}, line...)
					if !sendScanItem(scanItem{line: line}) {
						return
					}
				}
				if err != nil {
					if err == io.EOF {
						err = nil
					}
					_ = sendScanItem(scanItem{err: err, eof: true})
					return
				}
			}
		}()

		ticker := time.NewTicker(maxFlushWait)
		defer ticker.Stop()
		currentType := initialType
		var pending *LineResult
		pendingChars := 0

		sendResult := func(r LineResult) bool {
			select {
			case out <- r:
				return true
			case <-ctx.Done():
				done <- ctx.Err()
				return false
			}
		}

		flushPending := func() bool {
			if pending == nil {
				return true
			}
			if !sendResult(*pending) {
				return false
			}
			pending = nil
			pendingChars = 0
			return true
		}

		mergePending := func(r LineResult) {
			if pending == nil {
				p := r
				pending = &p
				pendingChars = 0
				for _, part := range r.Parts {
					pendingChars += len(part.Text)
				}
				return
			}
			pending.Parts = append(pending.Parts, r.Parts...)
			pending.ToolDetectionThinkingParts = append(pending.ToolDetectionThinkingParts, r.ToolDetectionThinkingParts...)
			pending.Stop = r.Stop
			pending.NextType = r.NextType
			pending.ResponseMessageID = r.ResponseMessageID
			for _, part := range r.Parts {
				pendingChars += len(part.Text)
			}
		}

		for {
			select {
			case item, ok := <-lineCh:
				if !ok {
					_ = flushPending()
					done <- nil
					return
				}
				if item.err != nil {
					_ = flushPending()
					done <- item.err
					return
				}
				if item.eof {
					_ = flushPending()
					done <- nil
					return
				}
				result := ParseDeepSeekContentLine(item.line, thinkingEnabled, currentType)
				currentType = result.NextType
				if !result.Parsed {
					continue
				}
				// Control events flush pending then send immediately.
				if result.Stop || result.ErrorMessage != "" || result.ContentFilter {
					if !flushPending() {
						return
					}
					if !sendResult(result) {
						return
					}
					continue
				}
				// Accumulate content chunks.
				mergePending(result)
				if pendingChars >= minFlushChars {
					if !flushPending() {
						return
					}
				}
			case <-ticker.C:
				if !flushPending() {
					return
				}
			case <-ctx.Done():
				done <- ctx.Err()
				return
			}
		}
	}()
	return out, done
}
