package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestDockerUpdateEntrypoint(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("需要 POSIX shell")
	}
	entrypoint, err := os.ReadFile("../docker/entrypoint.sh")
	if err != nil {
		t.Fatal(err)
	}
	mv, err := exec.LookPath("mv")
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"success", "corrupt", "replace failure"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			write := func(name, content string) {
				t.Helper()
				path := filepath.Join(root, name)
				if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(content), 0755); err != nil {
					t.Fatal(err)
				}
			}
			write("entrypoint.sh", strings.ReplaceAll(string(entrypoint), "/app", root))
			write("QMediaSync", "#!/bin/sh\necho old >> \"$QMS_TEST_RECORD\"\n")
			write("web_statics/index.html", "old-web")
			write("scripts/watch_update.sh", "#!/bin/sh\nexit 0\n")
			write("scripts/docker-entrypoint.sh", "old-entrypoint")
			archive, err := os.Create(filepath.Join(root, "qms.update.tar.gz"))
			if err != nil {
				t.Fatal(err)
			}
			gz := gzip.NewWriter(archive)
			tw := tar.NewWriter(gz)
			for name, body := range map[string]string{
				"QMediaSync":                   "#!/bin/sh\necho new >> \"$QMS_TEST_RECORD\"\n",
				"web_statics/index.html":       "new-web",
				"scripts/docker-entrypoint.sh": "new-entrypoint",
				"scripts/watch_update.sh":      "#!/bin/sh\nexit 0\n",
			} {
				if err := tw.WriteHeader(&tar.Header{Name: name, Size: int64(len(body)), Mode: 0755}); err != nil {
					t.Fatal(err)
				}
				if _, err := tw.Write([]byte(body)); err != nil {
					t.Fatal(err)
				}
			}
			if err := errors.Join(tw.Close(), gz.Close(), archive.Close()); err != nil {
				t.Fatal(err)
			}
			if scenario == "corrupt" {
				write("qms.update.tar.gz", "corrupt")
			}
			if scenario == "replace failure" {
				write("bin/mv", "#!/bin/sh\ncase \"$1\" in */update/web_statics) exit 1;; esac\nexec \"$QMS_TEST_MV\" \"$@\"\n")
			}
			t.Setenv("GUID", "")
			t.Setenv("GPID", "")
			t.Setenv("QMS_TEST_RECORD", filepath.Join(root, "starts"))
			t.Setenv("QMS_TEST_MV", mv)
			t.Setenv("PATH", filepath.Join(root, "bin")+string(os.PathListSeparator)+os.Getenv("PATH"))
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			output, err := exec.CommandContext(ctx, "sh", filepath.Join(root, "entrypoint.sh")).CombinedOutput()
			if err != nil {
				t.Fatalf("entrypoint: %v\n%s", err, output)
			}
			starts, err := os.ReadFile(filepath.Join(root, "starts"))
			if err != nil {
				t.Fatal(err)
			}
			wantStarts, wantWeb := "old\nold\n", "old-web"
			if scenario == "success" {
				wantStarts, wantWeb = "old\nnew\n", "new-web"
			}
			if string(starts) != wantStarts {
				t.Fatalf("启动记录 = %q，期望 %q\n%s", starts, wantStarts, output)
			}
			web, err := os.ReadFile(filepath.Join(root, "web_statics/index.html"))
			if err != nil || string(web) != wantWeb {
				t.Fatalf("web = %q, %v", web, err)
			}
			if scenario != "success" && strings.Contains(string(output), "更新完成，") {
				t.Fatalf("失败时误报完成：%s", output)
			}
			if _, err := os.Stat(filepath.Join(root, "qms.update.tar.gz")); !os.IsNotExist(err) {
				t.Fatal("旧更新包未清理")
			}
		})
	}
}
