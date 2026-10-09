package emby

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func TestEmbyAncestorCacheCoalescesConcurrentReads(t *testing.T) {
	for _, tc := range []struct {
		name   string
		failed bool
	}{{name: "success"}, {name: "failure", failed: true}} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var cache embyAncestorCache
				var calls atomic.Int32
				started, release := make(chan struct{}, 2), make(chan struct{})
				unblock := sync.OnceFunc(func() { close(release) })
				defer unblock()
				load := func(ctx context.Context) (embyAncestorObservation, error) {
					calls.Add(1)
					started <- struct{}{}
					select {
					case <-ctx.Done():
						return embyAncestorObservation{}, ctx.Err()
					case <-release:
						return embyAncestorObservation{At: time.Now(), Failed: tc.failed}, nil
					}
				}
				done := make(chan error, 2)
				read := func() {
					observation, err := cache.get(t.Context(), "folder", load)
					if err == nil && observation.Failed != tc.failed {
						t.Errorf("shared observation lost failure state: %+v", observation)
					}
					done <- err
				}
				go read()
				<-started
				go read()
				synctest.Wait()
				if calls.Load() != 1 {
					t.Fatalf("same folder was read concurrently %d times", calls.Load())
				}
				unblock()
				for range 2 {
					if err := <-done; err != nil {
						t.Fatal(err)
					}
				}
				if observation, err := cache.get(t.Context(), "folder", load); err != nil || observation.Failed != tc.failed || calls.Load() != 1 {
					t.Fatalf("completed observation was not reused: observation=%+v calls=%d err=%v", observation, calls.Load(), err)
				}
			})
		})
	}
}

func TestEmbyAncestorCacheDifferentFoldersReadConcurrently(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var cache embyAncestorCache
		var calls atomic.Int32
		release := make(chan struct{})
		unblock := sync.OnceFunc(func() { close(release) })
		defer unblock()
		load := func(ctx context.Context) (embyAncestorObservation, error) {
			calls.Add(1)
			select {
			case <-ctx.Done():
				return embyAncestorObservation{}, ctx.Err()
			case <-release:
				return embyAncestorObservation{At: time.Now()}, nil
			}
		}
		done := make(chan error, 2)
		for _, key := range []string{"first", "second"} {
			go func() {
				_, err := cache.get(t.Context(), key, load)
				done <- err
			}()
		}
		synctest.Wait()
		if calls.Load() != 2 {
			t.Fatalf("independent folder reads were serialized: calls=%d", calls.Load())
		}
		unblock()
		for range 2 {
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		}
	})
}

func TestEmbyAncestorCacheWaiterCancellationKeepsReader(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var cache embyAncestorCache
		var calls atomic.Int32
		started, release := make(chan struct{}, 2), make(chan struct{})
		unblock := sync.OnceFunc(func() { close(release) })
		defer unblock()
		load := func(ctx context.Context) (embyAncestorObservation, error) {
			calls.Add(1)
			started <- struct{}{}
			select {
			case <-ctx.Done():
				return embyAncestorObservation{}, ctx.Err()
			case <-release:
				return embyAncestorObservation{At: time.Now()}, nil
			}
		}
		readerDone := make(chan error, 1)
		go func() {
			_, err := cache.get(t.Context(), "folder", load)
			readerDone <- err
		}()
		<-started
		waiter, cancel := context.WithCancel(t.Context())
		defer cancel()
		waiterDone := make(chan error, 1)
		go func() {
			_, err := cache.get(waiter, "folder", load)
			waiterDone <- err
		}()
		synctest.Wait()
		cancel()
		if err := <-waiterDone; !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled waiter did not exit: %v", err)
		}
		if calls.Load() != 1 {
			t.Fatalf("waiter started a duplicate read: %d", calls.Load())
		}
		select {
		case err := <-readerDone:
			t.Fatalf("waiter cancellation stopped the shared read: %v", err)
		default:
		}
		unblock()
		if err := <-readerDone; err != nil {
			t.Fatal(err)
		}
		if _, err := cache.get(t.Context(), "folder", load); err != nil || calls.Load() != 1 {
			t.Fatalf("cancelled waiter discarded the completed observation: calls=%d err=%v", calls.Load(), err)
		}
		if _, err := cache.get(waiter, "folder", load); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled caller accepted a cached observation: %v", err)
		}
	})
}

func TestEmbyAncestorCacheReaderCancellationDoesNotCacheFailure(t *testing.T) {
	for _, tc := range []struct {
		name   string
		shared bool
	}{{name: "independent waiter"}, {name: "shared cancellation", shared: true}} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var cache embyAncestorCache
				var calls atomic.Int32
				reader, cancel := context.WithCancel(t.Context())
				defer cancel()
				started := make(chan struct{})
				load := func(ctx context.Context) (embyAncestorObservation, error) {
					if calls.Add(1) == 1 {
						close(started)
						<-ctx.Done()
						return embyAncestorObservation{}, ctx.Err()
					}
					return embyAncestorObservation{At: time.Now()}, nil
				}
				readerDone := make(chan error, 1)
				go func() {
					_, err := cache.get(reader, "folder", load)
					readerDone <- err
				}()
				<-started
				waiter := t.Context()
				if tc.shared {
					waiter = reader
				}
				waiterDone := make(chan error, 1)
				go func() {
					_, err := cache.get(waiter, "folder", load)
					waiterDone <- err
				}()
				synctest.Wait()
				cancel()
				if err := <-readerDone; !errors.Is(err, context.Canceled) {
					t.Fatalf("cancelled reader did not exit: %v", err)
				}
				if err := <-waiterDone; tc.shared && !errors.Is(err, context.Canceled) || !tc.shared && err != nil {
					t.Fatalf("waiter did not follow its own context: %v", err)
				}
				if tc.shared && calls.Load() != 1 {
					t.Fatalf("shared cancellation started another read: %d", calls.Load())
				}
				if observation, err := cache.get(t.Context(), "folder", load); err != nil || observation.Failed || calls.Load() != 2 {
					t.Fatalf("cancelled read poisoned retry: observation=%+v calls=%d err=%v", observation, calls.Load(), err)
				}
			})
		})
	}
}

func TestEmbyAncestorCacheHitsDoNotExtendExpiry(t *testing.T) {
	for _, tc := range []struct {
		name   string
		failed bool
	}{{name: "success"}, {name: "failure", failed: true}} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var cache embyAncestorCache
				calls := 0
				load := func(context.Context) (embyAncestorObservation, error) {
					calls++
					return embyAncestorObservation{At: time.Now(), Failed: tc.failed}, nil
				}
				first, err := cache.get(t.Context(), "folder", load)
				if err != nil {
					t.Fatal(err)
				}
				time.Sleep(29 * time.Second)
				if hit, err := cache.get(t.Context(), "folder", load); err != nil || !hit.At.Equal(first.At) || hit.Failed != tc.failed || calls != 1 {
					t.Fatalf("cache hit renewed or changed the observation: observation=%+v calls=%d err=%v", hit, calls, err)
				}
				time.Sleep(time.Second)
				if refreshed, err := cache.get(t.Context(), "folder", load); err != nil || !refreshed.At.After(first.At) || refreshed.Failed != tc.failed || calls != 2 {
					t.Fatalf("30-second observation was not refreshed: observation=%+v calls=%d err=%v", refreshed, calls, err)
				}
			})
		})
	}
}
