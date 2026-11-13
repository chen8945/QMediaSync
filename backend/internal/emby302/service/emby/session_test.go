package emby

import (
	"context"
	"net/http"
	"testing"
	"time"

	"qmediasync/internal/emby302/config"
	"qmediasync/internal/emby302/util/https"
	"qmediasync/internal/emby302/web/cache"
)

func newTestSession(t *testing.T) *Session {
	t.Helper()
	origin := ""
	if config.C != nil && config.C.Emby != nil {
		origin = config.C.Emby.Host
	}
	client := https.NewClient(https.ClientOptions{})
	t.Cleanup(client.CloseIdleConnections)
	return newTestSessionWithClient(t, origin, client, PreloadOptions{})
}

func newTestSessionWithClient(t *testing.T, origin string, client *http.Client, preload PreloadOptions) *Session {
	t.Helper()
	expiry := time.Hour
	if config.C != nil && config.C.Cache != nil {
		expiry = config.C.Cache.ExpiredDuration()
	}
	store := cache.NewStore(expiry)
	session := NewSession(SessionOptions{Origin: origin, Client: client, Cache: store.NewScope(), Preload: preload,
		RunAuxiliary: func(work func(context.Context)) bool { work(t.Context()); return true },
	})
	t.Cleanup(func() {
		session.Close()
		if err := store.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	})
	return session
}
