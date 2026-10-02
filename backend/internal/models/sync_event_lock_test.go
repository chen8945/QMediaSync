package models

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
)

func TestSyncEventLocksCancellationAndIndependentIDs(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var locks syncEventLocks
		release, err := locks.acquire(t.Context(), 1)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		result := make(chan error, 1)
		go func() {
			unlock, err := locks.acquire(ctx, 1)
			if unlock != nil {
				unlock()
			}
			result <- err
		}()
		synctest.Wait()
		other, err := locks.acquire(t.Context(), 2)
		if err != nil {
			t.Fatal(err)
		}
		other()
		cancel()
		if err := <-result; !errors.Is(err, context.Canceled) {
			t.Fatalf("waiting acquisition: %v", err)
		}
		release()
		next, err := locks.acquire(t.Context(), 1)
		if err != nil {
			t.Fatal(err)
		}
		release() // 重复释放不能释放下一位持有者。
		blocked, stop := context.WithCancel(t.Context())
		stop()
		if _, err := locks.acquire(blocked, 1); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled acquisition: %v", err)
		}
		next()
		if len(locks.active) != 0 {
			t.Fatal("released IDs were retained")
		}
	})
}
