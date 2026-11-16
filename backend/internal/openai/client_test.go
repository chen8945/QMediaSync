package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestChatCompletionFailureRetainsMessageWithoutPrintingBody(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	previousStdout := os.Stdout
	os.Stdout = writer
	t.Cleanup(func() {
		os.Stdout = previousStdout
		writer.Close()
		reader.Close()
	})
	for _, test := range []struct {
		name, body, message string
		status              int
	}{
		{"nested-string-code", `{"error":{"message":"quota detail","code":"insufficient_quota"},"private":"body-private"}`, "quota detail", http.StatusTooManyRequests},
		{"nested-null-code", `{"error":{"message":"provider detail","code":null}}`, "provider detail", http.StatusUnauthorized},
		{"nested-number-code", `{"error":{"message":"nested detail","code":123},"message":"legacy detail"}`, "nested detail", http.StatusBadRequest},
		{"legacy-message", `{"message":"legacy detail","code":123}`, "legacy detail", http.StatusBadRequest},
		{"unknown-json", `{"private":"body-private"}`, "", http.StatusBadGateway},
		{"malformed-json", `{"private":"body-private"`, "", http.StatusBadGateway},
		{"empty-body", "", "", http.StatusServiceUnavailable},
		{"blank-message", `{"error":{"message":"  "}}`, "", http.StatusBadGateway},
		{"invalid-error-shape", `{"error":"body-private"}`, "", http.StatusBadGateway},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(test.status)
				io.WriteString(w, test.body)
			}))
			defer server.Close()
			client := NewClient("key", server.URL, "model", 5)
			defer client.Close()
			result, err := client.CreateChatCompletion(context.Background(), []Message{{Role: "user", Content: "fixture"}}, nil)
			if result != nil || err == nil || strings.TrimSpace(err.Error()) == "" {
				t.Fatalf("failed completion returned no usable error: %+v %v", result, err)
			}
			if test.message != "" {
				if err.Error() != test.message {
					t.Fatalf("provider message lost: %v", err)
				}
			} else if !strings.Contains(err.Error(), http.StatusText(test.status)) && !strings.Contains(err.Error(), fmt.Sprint(test.status)) {
				t.Fatalf("fallback lost HTTP status: %v", err)
			}
			if strings.Contains(err.Error(), "body-private") {
				t.Fatal("error retained the complete provider body")
			}
		})
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	os.Stdout = previousStdout
	printed, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if len(printed) != 0 {
		t.Fatal("client printed provider failures outside the safe diagnostic boundary")
	}
}

func TestClientsKeepKeyModelAndPrompt(t *testing.T) {
	type received struct {
		auth string
		body ChatCompletionRequest
	}
	requests := make(chan received, 3)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body ChatCompletionRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		requests <- received{r.Header.Get("Authorization"), body}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"name\":\"movie\",\"year\":2024}"}}]}`))
	}))
	defer server.Close()
	saved := NewClient("saved", server.URL, "saved-model", 60)
	defer saved.Close()
	draft := NewClient("draft", server.URL, "draft-model", 5)
	defer draft.Close()
	for _, operation := range []struct {
		client     *Client
		key, model string
	}{{saved, "saved", "saved-model"}, {draft, "draft", "draft-model"}, {saved, "saved", "saved-model"}} {
		info, err := operation.client.TakeMoiveName(context.Background(), "movie.2024.mkv", "keep the original title")
		if err != nil || info.Name != "movie" || info.Year != 2024 {
			t.Fatalf("recognition: %+v %v", info, err)
		}
		got := <-requests
		if got.auth != "Bearer "+operation.key || got.body.Model != operation.model || len(got.body.Messages) != 1 || !strings.Contains(got.body.Messages[0].Content, "keep the original title") || !strings.Contains(got.body.Messages[0].Content, "movie.2024.mkv") {
			t.Fatalf("captured config/prompt: %+v", got)
		}
	}
}

func TestClientTimeoutAndCancellationReachHTTPRequest(t *testing.T) {
	started := make(chan struct{}, 3)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		started <- struct{}{}
		<-r.Context().Done()
	}))
	defer server.Close()
	client := NewClient("key", server.URL, "model", 1)
	defer client.Close()
	result := make(chan error, 1)
	go func() { _, err := client.TakeMoiveName(context.Background(), "movie", "prompt"); result <- err }()
	select {
	case err := <-result:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("configured timeout: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("configured client timeout was replaced by request defaults")
	}
	ctx, cancel := context.WithCancel(context.Background())
	for len(started) > 0 {
		<-started
	}
	go func() { _, err := client.TakeMoiveName(ctx, "movie", "prompt"); result <- err }()
	<-started
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("request cancellation: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation kept retrying")
	}
}

func TestEmptyChoicesReturnsError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[]}`))
	}))
	defer server.Close()
	client := NewClient("key", server.URL, "model", 5)
	defer client.Close()
	if _, err := client.TakeMoiveName(context.Background(), "movie", "prompt"); err == nil {
		t.Fatal("empty choices succeeded")
	}
}
