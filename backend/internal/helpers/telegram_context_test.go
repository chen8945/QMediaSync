package helpers

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

func TestTelegramLifetimeCancelsActualPollingHTTP(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	polling, returned := make(chan struct{}), make(chan struct{})
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/bot-token/getMe" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true,"result":{"id":1,"is_bot":true,"first_name":"test"}}`))
			return
		}
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			return
		}
		close(polling)
		<-r.Context().Done()
		close(returned)
	}))
	defer func() { cancel(); remote.Close() }()
	client := remote.Client()
	api, err := tgbotapi.NewBotAPIWithClient("-token", remote.URL+"/bot%s/%s", telegramHTTPClient{ctx: ctx, Client: client})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := api.GetUpdates(tgbotapi.NewUpdate(0)); done <- err }()
	<-polling
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("poll error=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("poll did not cancel")
	}
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("upstream request remained active")
	}
}
