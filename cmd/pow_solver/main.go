package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"ds2api/pow"
)

func main() {
	var payload struct {
		Algorithm  string  `json:"algorithm"`
		Challenge  string  `json:"challenge"`
		Salt       string  `json:"salt"`
		ExpireAt   int64   `json:"expire_at"`
		ExpireAtAlt int64  `json:"expireAt"`
		Difficulty float64 `json:"difficulty"`
		Signature  string  `json:"signature"`
		TargetPath string  `json:"target_path"`
	}
	if err := json.NewDecoder(os.Stdin).Decode(&payload); err != nil {
		fmt.Fprintf(os.Stderr, "failed to parse challenge JSON: %v\n", err)
		os.Exit(1)
	}
	expireAt := payload.ExpireAt
	if expireAt == 0 {
		expireAt = payload.ExpireAtAlt
	}
	difficulty := int64(payload.Difficulty)
	if difficulty == 0 {
		difficulty = 144000
	}
	answer, err := pow.SolvePow(context.Background(), payload.Challenge, payload.Salt, expireAt, difficulty)
	if err != nil {
		fmt.Fprintf(os.Stderr, "pow solve failed: %v\n", err)
		os.Exit(1)
	}
	out := map[string]any{
		"algorithm":  payload.Algorithm,
		"challenge":  payload.Challenge,
		"salt":       payload.Salt,
		"answer":     answer,
		"signature":  payload.Signature,
		"target_path": payload.TargetPath,
	}
	if err := json.NewEncoder(os.Stdout).Encode(out); err != nil {
		fmt.Fprintf(os.Stderr, "failed to write result: %v\n", err)
		os.Exit(1)
	}
}
