package syncstrm

import (
	"testing"
	"time"

	"qmediasync/internal/models"
)

func TestShouldPublishProgressHonorsIntervalAndForce(t *testing.T) {
	s := &SyncStrm{
		Sync: &models.Sync{ID: 3},
	}
	now := time.Unix(100, 0)

	if !s.shouldPublishProgress(now, false) {
		t.Fatal("首次进度应允许发布")
	}
	s.markProgressPublished(now)

	if s.shouldPublishProgress(now.Add(200*time.Millisecond), false) {
		t.Fatal("间隔不足时不应发布")
	}
	if !s.shouldPublishProgress(now.Add(200*time.Millisecond), true) {
		t.Fatal("force=true 时应发布")
	}
	if !s.shouldPublishProgress(now.Add(2*time.Second), false) {
		t.Fatal("超过间隔时应发布")
	}
}
