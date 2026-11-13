package cache

import (
	"testing"
	"time"
)

func TestSpaceCleanupPreservesNewerResponse(t *testing.T) {
	scope := newCacheScope(t)
	space := t.Name()

	old := &respCache{expired: time.Now().Add(-time.Second).UnixMilli()}
	current := &respCache{expired: time.Now().Add(time.Minute).UnixMilli()}
	scope.store.mu.Lock()
	scope.putSpaceCacheLocked(space, "item", old)
	scope.putSpaceCacheLocked(space, "item", current)
	scope.delSpaceCacheLocked(space, "item", old)
	scope.store.mu.Unlock()
	if got, ok := scope.GetSpaceCache(space, "item"); !ok || got != current {
		t.Fatal("cleaning the expired response removed its replacement")
	}
}
