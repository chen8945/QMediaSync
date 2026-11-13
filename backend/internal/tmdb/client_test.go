package tmdb

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"golang.org/x/time/rate"
)

func TestClientsKeepCapturedConfigurationAndClosePrivatePool(t *testing.T) {
	var mu sync.Mutex
	var requests []string
	closed := make(chan struct{}, 2)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests = append(requests, r.URL.Query().Get("api_key")+":"+r.URL.Query().Get("language")+":"+r.RemoteAddr)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":1,"title":"movie"}`))
	}))
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateClosed {
			closed <- struct{}{}
		}
	}
	server.Start()
	defer server.Close()
	first, err := NewClient("saved", "", server.URL, "zh-CN", "")
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	draft, err := NewClient("draft", "", server.URL, "en-US", "")
	if err != nil {
		t.Fatal(err)
	}
	defer draft.Close()
	for _, operation := range []struct {
		client   *Client
		language string
	}{{first, "zh-CN"}, {draft, "en-US"}, {first, "zh-CN"}} {
		if _, err := operation.client.GetMovieDetail(context.Background(), 1, operation.language); err != nil {
			t.Fatal(err)
		}
	}
	mu.Lock()
	if len(requests) != 3 || requests[0] != requests[2] || requests[0] == requests[1] {
		t.Errorf("captured configuration / connection reuse: %v", requests)
	}
	mu.Unlock()
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("private idle connection remained open")
	}
	if _, err := draft.GetMovieDetail(context.Background(), 1, "en-US"); err != nil {
		t.Fatalf("closing another scope affected draft: %v", err)
	}
}

func TestClientsShareRequestQuota(t *testing.T) {
	old := requestLimiter
	requestLimiter = rate.NewLimiter(rate.Every(time.Hour), 1)
	defer func() { requestLimiter = old }()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":1}`))
	}))
	defer server.Close()
	first, err := NewClient("first", "", server.URL, "zh-CN", "")
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := NewClient("second", "", server.URL, "zh-CN", "")
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if _, err := first.GetMovieDetail(context.Background(), 1, "zh-CN"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err = second.GetMovieDetail(ctx, 1, "zh-CN")
	if err == nil {
		t.Fatal("a new client reset the shared quota")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("quota retry ignored cancellation: %v", err)
	}
}
