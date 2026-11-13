package embyclientrestgo

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
)

type connectionWaitingContext struct {
	context.Context
	waiting chan struct{}
	once    sync.Once
}

func (ctx *connectionWaitingContext) Done() <-chan struct{} {
	ctx.once.Do(func() { close(ctx.waiting) })
	return ctx.Context.Done()
}

func TestConnectionWaiterRecoversAfterInitiatorCancellation(t *testing.T) {
	for _, identity := range []bool{false, true} {
		t.Run(fmt.Sprintf("identity=%v", identity), func(t *testing.T) {
			started := make(chan struct{})
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if requests.Add(1) == 1 {
					close(started)
					<-r.Context().Done()
					return
				}
				if identity {
					fmt.Fprint(w, `{"Id":"original-server","ProductName":"Emby Server"}`)
				} else {
					fmt.Fprint(w, "Emby Server")
				}
			}))
			defer server.Close()
			client := NewClientForProduct(server.URL, "test-key", ProductEmby)
			check := func(ctx context.Context) error {
				if identity {
					id, err := client.GetServerID(ctx)
					if err == nil && id != "original-server" {
						return fmt.Errorf("unexpected identity %q", id)
					}
					return err
				}
				info, err := client.CheckConnection(ctx)
				if err == nil && info.Product != ProductEmby {
					return fmt.Errorf("unexpected product %q", info.Product)
				}
				return err
			}
			first, cancel := context.WithCancel(t.Context())
			defer cancel()
			firstResult := make(chan error, 1)
			go func() { firstResult <- check(first) }()
			<-started
			waiter := &connectionWaitingContext{Context: t.Context(), waiting: make(chan struct{})}
			nextResult := make(chan error, 1)
			go func() { nextResult <- check(waiter) }()
			<-waiter.waiting
			cancel()
			if err := <-firstResult; !errors.Is(err, context.Canceled) {
				t.Fatalf("initiator error: %v", err)
			}
			if err := <-nextResult; err != nil || waiter.Err() != nil {
				t.Fatalf("valid waiter inherited cancellation: own=%v result=%v", waiter.Err(), err)
			}
			if requests.Load() != 2 {
				t.Fatalf("requests=%d, want one cancelled and one replacement probe", requests.Load())
			}
		})
	}
}
