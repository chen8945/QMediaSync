package cache

import (
	"testing"
	"time"
)

func TestSpaceCleanupPreservesNewerResponse(t *testing.T) {
	space := t.Name()
	t.Cleanup(func() { spaceMap.Delete(space) })
	old := &respCache{expired: time.Now().Add(-time.Second).UnixMilli()}
	current := &respCache{expired: time.Now().Add(time.Minute).UnixMilli()}
	putSpaceCache(space, "item", old)
	putSpaceCache(space, "item", current)
	delSpaceCache(space, "item", old)
	if got, ok := GetSpaceCache(space, "item"); !ok || got != current {
		t.Fatal("cleaning the expired response removed its replacement")
	}
}
