package requestbody

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestValidateJSONUTF8RejectsInvalidJSONBody(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader("{\"x\":\"\xff\"}"))
	req.Header.Set("Content-Type", "application/json")
	var gotErr error
	ValidateJSONUTF8(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		_, gotErr = io.ReadAll(r.Body)
	})).ServeHTTP(httptest.NewRecorder(), req)

	if !errors.Is(gotErr, ErrInvalidUTF8Body) {
		t.Fatalf("expected ErrInvalidUTF8Body, got %v", gotErr)
	}
}

func TestValidateJSONUTF8ReplaysValidJSONBody(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"x":"中文"}`))
	req.Header.Set("Content-Type", "application/json")
	var got string
	ValidateJSONUTF8(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("unexpected read error: %v", err)
		}
		got = string(raw)
	})).ServeHTTP(httptest.NewRecorder(), req)

	if got != `{"x":"中文"}` {
		t.Fatalf("body replay mismatch: %q", got)
	}
}

func TestValidateJSONUTF8SkipsMultipartUpload(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/files", strings.NewReader("x=\xff"))
	req.Header.Set("Content-Type", "multipart/form-data; boundary=x")
	var got string
	ValidateJSONUTF8(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("unexpected read error: %v", err)
		}
		got = string(raw)
	})).ServeHTTP(httptest.NewRecorder(), req)

	if got != "x=\xff" {
		t.Fatalf("expected multipart body to pass through, got %q", got)
	}
}
