package helpers

import (
	"crypto/md5"
	"crypto/sha1"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMetadataSafeWrite(t *testing.T) {
	for _, mode := range []string{"replace", "empty", "length", "sha1", "md5", "read", "save", "changed", "create_exists", "unknown"} {
		t.Run(mode, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "movie.nfo")
			if err := os.WriteFile(path, []byte("old"), 0644); err != nil {
				t.Fatal(err)
			}
			oldTime := time.Unix(50, 0)
			if err := os.Chtimes(path, oldTime, oldTime); err != nil {
				t.Fatal(err)
			}
			baseline, err := MetadataFingerprint(path)
			if err != nil {
				t.Fatal(err)
			}
			content := "new"
			size := int64(3)
			want := "old"
			if mode == "empty" {
				content = ""
				size = 0
			}
			if mode == "length" {
				size = 4
			}
			if mode == "unknown" {
				size = -1
			}
			sha := fmt.Sprintf("%x", sha1.Sum([]byte(content)))
			md := fmt.Sprintf("%x", md5.Sum([]byte(content)))
			if mode == "sha1" {
				sha = "bad"
			}
			if mode == "md5" {
				md = "bad"
			}
			if mode == "create_exists" {
				baseline = ""
			}
			err = WriteMetadataFile(path, baseline, size, sha, md, 100, func(w io.Writer) error {
				if _, err := io.WriteString(w, content); err != nil {
					return err
				}
				if mode == "read" {
					return io.ErrUnexpectedEOF
				}
				if mode == "changed" {
					want = "user"
					return os.WriteFile(path, []byte(want), 0644)
				}
				return nil
			}, func(string) error {
				if mode == "save" {
					return errors.New("save failed")
				}
				return nil
			})
			success := mode == "replace" || mode == "empty" || mode == "unknown"
			if success {
				want = content
			}
			if (err == nil) != success {
				t.Fatalf("error=%v success=%v", err, success)
			}
			got, _ := os.ReadFile(path)
			if string(got) != want {
				t.Fatalf("got %q want %q", got, want)
			}
			if !success && mode != "changed" {
				info, _ := os.Stat(path)
				if !info.ModTime().Equal(oldTime) {
					t.Fatal("old mtime changed")
				}
			}
			files, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".qms-metadata-*"))
			if len(files) != 0 {
				t.Fatalf("temp files: %v", files)
			}
		})
	}
}

func TestMetadataHTTPFailurePreservesTarget(t *testing.T) {
	for _, mode := range []string{"status", "truncated", "chunked", "redirect"} {
		t.Run(mode, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "a.nfo")
			os.WriteFile(path, []byte("old"), 0644)
			baseline, _ := MetadataFingerprint(path)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.UserAgent() != "metadata-test" {
					t.Error("missing UA")
				}
				if mode == "status" {
					w.WriteHeader(500)
					return
				}
				if mode == "truncated" {
					w.Header().Set("Content-Length", "20")
					io.WriteString(w, "short")
					return
				}
				if mode == "redirect" && r.URL.Path == "/" {
					http.Redirect(w, r, "http://"+r.Host+"/file", 302)
					return
				}
				w.(http.Flusher).Flush()
				io.WriteString(w, "new")
			}))
			defer server.Close()
			err := DownloadMetadataFile(server.URL, path, "metadata-test", baseline, 3, "", "", 100, nil)
			success := mode == "chunked" || mode == "redirect"
			if (err == nil) != success {
				t.Fatalf("err=%v", err)
			}
			got, _ := os.ReadFile(path)
			want := "old"
			if success {
				want = "new"
			}
			if string(got) != want {
				t.Fatalf("got %q", got)
			}
		})
	}
}

func TestMetadataConcurrentPublish(t *testing.T) {
	for _, replace := range []bool{false, true} {
		t.Run(fmt.Sprint(replace), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "movie.nfo")
			baseline := ""
			if replace {
				if err := os.WriteFile(path, []byte("old"), 0644); err != nil {
					t.Fatal(err)
				}
				var err error
				baseline, err = MetadataFingerprint(path)
				if err != nil {
					t.Fatal(err)
				}
			}
			ready := make(chan struct{}, 2)
			publish := make(chan struct{})
			results := make(chan error, 2)
			aliasDir := filepath.Join(t.TempDir(), "alias")
			if err := os.Symlink(filepath.Dir(path), aliasDir); err != nil {
				t.Fatal(err)
			}
			for i, content := range []string{"one", "two"} {
				target := path
				if i == 1 {
					target = filepath.Join(aliasDir, filepath.Base(path))
				}
				go func() {
					results <- WriteMetadataFile(target, baseline, 3, "", "", 100, func(w io.Writer) error {
						_, err := io.WriteString(w, content)
						return err
					}, func(string) error { ready <- struct{}{}; <-publish; return nil })
				}()
			}
			<-ready
			<-ready
			close(publish)
			first, second := <-results, <-results
			if (first == nil) == (second == nil) {
				t.Fatalf("expected one publisher: %v, %v", first, second)
			}
			got, err := os.ReadFile(path)
			if err != nil || (string(got) != "one" && string(got) != "two") {
				t.Fatalf("content=%q err=%v", got, err)
			}
			files, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".qms-metadata-*"))
			if err != nil || len(files) != 0 {
				t.Fatalf("temporary files=%v err=%v", files, err)
			}
		})
	}
}

func TestMetadataRejectsSymlinkTarget(t *testing.T) {
	dir := t.TempDir()
	target, link := filepath.Join(dir, "target"), filepath.Join(dir, "link")
	if err := os.WriteFile(target, []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}
	baseline, err := MetadataFingerprint(target)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := MetadataFingerprint(link); err == nil {
		t.Fatal("symlink fingerprint accepted")
	}
	err = WriteMetadataFile(link, baseline, 3, "", "", 100, func(w io.Writer) error { _, err := io.WriteString(w, "new"); return err }, nil)
	if err == nil {
		t.Fatal("symlink replacement accepted")
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != "old" {
		t.Fatalf("target=%q err=%v", got, err)
	}
}

func TestMetadataCopySymlinkSource(t *testing.T) {
	for _, mode := range []string{"copy", "retarget", "replace_file", "change_content", "change_time", "regular_to_link"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			source, link := filepath.Join(dir, "source.nfo"), filepath.Join(dir, "linked.nfo")
			target := filepath.Join(t.TempDir(), "target.nfo")
			for path, content := range map[string]string{source: "new", target: "old"} {
				if err := os.WriteFile(path, []byte(content), 0644); err != nil {
					t.Fatal(err)
				}
			}
			mtime := time.Unix(100, 0)
			if err := os.Chtimes(source, mtime, mtime); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("source.nfo", link); err != nil {
				t.Fatal(err)
			}
			baseline, err := MetadataFingerprint(target)
			if err != nil {
				t.Fatal(err)
			}
			copySource := link
			if mode == "regular_to_link" {
				copySource = source
			}
			err = CopyMetadataFile(copySource, target, baseline, 3, 100, func(string) error {
				switch mode {
				case "regular_to_link":
					moved := filepath.Join(dir, "moved.nfo")
					if err := os.Rename(source, moved); err != nil {
						return err
					}
					return os.Symlink(moved, source)
				case "retarget", "replace_file":
					replacement := filepath.Join(dir, "replacement.nfo")
					if err := os.WriteFile(replacement, []byte("new"), 0644); err != nil {
						return err
					}
					if err := os.Chtimes(replacement, mtime, mtime); err != nil {
						return err
					}
					if mode == "replace_file" {
						return os.Rename(replacement, source)
					}
					if err := os.Remove(link); err != nil {
						return err
					}
					return os.Symlink(replacement, link)
				case "change_content":
					if err := os.WriteFile(source, []byte("bad"), 0644); err != nil {
						return err
					}
					return os.Chtimes(source, mtime, mtime)
				case "change_time":
					return os.Chtimes(source, mtime.Add(time.Second), mtime.Add(time.Second))
				}
				return nil
			})
			success := mode == "copy"
			if (err == nil) != success {
				t.Fatalf("复制结果：%v，期望成功=%v", err, success)
			}
			want := "old"
			if success {
				want = "new"
			}
			got, err := os.ReadFile(target)
			if err != nil || string(got) != want {
				t.Fatalf("目标内容=%q，错误=%v", got, err)
			}
			files, err := filepath.Glob(filepath.Join(filepath.Dir(target), ".qms-metadata-*"))
			if err != nil || len(files) != 0 {
				t.Fatalf("临时文件=%v，错误=%v", files, err)
			}
		})
	}
}

func TestMetadataPublishDifferentTargetsDoNotBlock(t *testing.T) {
	dir := t.TempDir()
	unlock, err := lockMetadataPublish(filepath.Join(dir, "busy.nfo"))
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	done := make(chan error, 1)
	go func() {
		done <- WriteMetadataFile(filepath.Join(dir, "other.nfo"), "", 3, "", "", 100, func(w io.Writer) error {
			_, err := io.WriteString(w, "new")
			return err
		}, nil)
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("independent target blocked by another publisher")
	}
}
