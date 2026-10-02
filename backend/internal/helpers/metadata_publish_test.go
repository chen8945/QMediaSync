//go:build linux || windows

package helpers

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestMetadataNativeNoReplace(t *testing.T) {
	for _, exists := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing", true: "exists"}[exists], func(t *testing.T) {
			dir := t.TempDir()
			source, target := filepath.Join(dir, "temp"), filepath.Join(dir, "movie.nfo")
			if err := os.WriteFile(source, []byte("complete"), 0600); err != nil {
				t.Fatal(err)
			}
			if exists {
				if err := os.WriteFile(target, []byte("user"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			err := renameMetadataNoReplace(source, target)
			if exists && !errors.Is(err, os.ErrExist) || !exists && err != nil {
				t.Fatalf("rename error=%v, exists=%v", err, exists)
			}
			want := "complete"
			if exists {
				want = "user"
				got, err := os.ReadFile(source)
				if err != nil || string(got) != "complete" {
					t.Fatalf("source=%q, err=%v", got, err)
				}
			} else if _, err := os.Stat(source); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("source remains after rename: %v", err)
			}
			got, err := os.ReadFile(target)
			if err != nil || string(got) != want {
				t.Fatalf("target=%q, want=%q, err=%v", got, want, err)
			}
		})
	}
}
