package openai

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

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
