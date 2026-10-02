package helpers

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestMetadataPublishFallback(t *testing.T) {
	for _, mode := range []string{"link", "unsupported", "exists", "racing_create", "permission", "operation_not_permitted", "io_error", "no_native_support"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			source, target := filepath.Join(dir, "temp"), filepath.Join(dir, "movie.nfo")
			if err := os.WriteFile(source, []byte("complete"), 0600); err != nil {
				t.Fatal(err)
			}
			if mode == "exists" {
				if err := os.WriteFile(target, []byte("user"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			linkErr := error(unix.EOPNOTSUPP)
			switch mode {
			case "permission":
				linkErr = unix.EACCES
			case "operation_not_permitted":
				linkErr = unix.EPERM
			case "io_error":
				linkErr = unix.EIO
			}
			renameCalled := false
			err := publishMissingMetadata(source, target, func(source, target string) error {
				if mode == "link" || mode == "exists" {
					return os.Link(source, target)
				}
				if mode == "racing_create" {
					// 模拟另一进程在硬链接失败后抢先发布，不依赖本进程发布锁。
					if err := os.WriteFile(target, []byte("user"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				return &os.LinkError{Op: "link", Old: source, New: target, Err: linkErr}
			}, func(source, target string) error {
				renameCalled = true
				if mode == "no_native_support" {
					return unix.ENOSYS
				}
				return renameMetadataNoReplace(source, target)
			})
			wantRename := mode == "unsupported" || mode == "racing_create" || mode == "no_native_support"
			if renameCalled != wantRename {
				t.Fatalf("rename called=%v, want=%v", renameCalled, wantRename)
			}
			success := mode == "link" || mode == "unsupported"
			if (err == nil) != success {
				t.Fatalf("error=%v, success=%v", err, success)
			}
			if mode == "exists" || mode == "racing_create" {
				if !errors.Is(err, os.ErrExist) {
					t.Fatalf("lost target-exists error: %v", err)
				}
			}
			if mode == "no_native_support" && (!errors.Is(err, unix.ENOSYS) || !strings.Contains(err.Error(), "硬链接失败")) {
				t.Fatalf("missing capability error: %v", err)
			}
			if mode == "permission" || mode == "operation_not_permitted" || mode == "io_error" {
				if !errors.Is(err, linkErr) {
					t.Fatalf("lost original failure: %v", err)
				}
			}
			got, readErr := os.ReadFile(target)
			switch {
			case success:
				if readErr != nil || string(got) != "complete" {
					t.Fatalf("published=%q, err=%v", got, readErr)
				}
			case mode == "exists" || mode == "racing_create":
				if readErr != nil || string(got) != "user" {
					t.Fatalf("user file=%q, err=%v", got, readErr)
				}
			default:
				if !errors.Is(readErr, os.ErrNotExist) {
					t.Fatalf("failed publish created target: %v", readErr)
				}
			}
		})
	}
}
