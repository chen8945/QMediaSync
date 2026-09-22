package helpers

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestZipDirReportsFinalWriteFailure(t *testing.T) {
	if _, err := os.Stat("/dev/full"); err != nil {
		t.Skip("需要 /dev/full 注入 ZIP 关闭时的写入错误")
	}
	if err := ZipDir(t.TempDir(), "/dev/full"); err == nil {
		t.Fatal("ZIP 中央目录写入失败必须返回错误")
	}
}

type testTarEntry struct {
	name     string
	typeflag byte
	linkname string
	body     string
}

func writeTestTarGz(t *testing.T, entries ...testTarEntry) string {
	t.Helper()

	archivePath := filepath.Join(t.TempDir(), "archive.tar.gz")
	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatalf("创建测试归档失败: %v", err)
	}
	defer file.Close()

	gzipWriter := gzip.NewWriter(file)
	defer gzipWriter.Close()

	tarWriter := tar.NewWriter(gzipWriter)
	defer tarWriter.Close()

	for _, entry := range entries {
		mode := int64(0644)
		if entry.typeflag == tar.TypeDir {
			mode = 0755
		}
		header := &tar.Header{
			Name:     entry.name,
			Typeflag: entry.typeflag,
			Mode:     mode,
			Linkname: entry.linkname,
			Size:     int64(len(entry.body)),
		}
		if err := tarWriter.WriteHeader(header); err != nil {
			t.Fatalf("写入 tar header 失败: %v", err)
		}
		if entry.body != "" {
			if _, err := tarWriter.Write([]byte(entry.body)); err != nil {
				t.Fatalf("写入 tar 内容失败: %v", err)
			}
		}
	}

	return archivePath
}

func TestExtractTarGz_解压普通文件(t *testing.T) {
	dst := filepath.Join(t.TempDir(), "dst")
	archivePath := writeTestTarGz(t,
		testTarEntry{name: "qms", typeflag: tar.TypeDir},
		testTarEntry{name: "qms/QMediaSync", typeflag: tar.TypeReg, body: "binary"},
	)

	if err := ExtractTarGz(archivePath, dst); err != nil {
		t.Fatalf("解压普通文件失败: %v", err)
	}

	content, err := os.ReadFile(filepath.Join(dst, "qms", "QMediaSync"))
	if err != nil {
		t.Fatalf("读取解压文件失败: %v", err)
	}
	if string(content) != "binary" {
		t.Fatalf("解压文件内容 = %q，期望 binary", string(content))
	}
}

func TestExtractTarGz_拒绝路径逃逸(t *testing.T) {
	testCases := []struct {
		name      string
		entryName string
	}{
		{name: "上级目录", entryName: "../evil.txt"},
		{name: "绝对路径", entryName: "/tmp/qms-evil.txt"},
		{name: "反斜杠上级目录", entryName: `..\evil.txt`},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			tempDir := t.TempDir()
			dst := filepath.Join(tempDir, "dst")
			outside := filepath.Join(tempDir, "evil.txt")
			archivePath := writeTestTarGz(t, testTarEntry{
				name:     tc.entryName,
				typeflag: tar.TypeReg,
				body:     "evil",
			})

			err := ExtractTarGz(archivePath, dst)
			if err == nil || !strings.Contains(err.Error(), "不安全") {
				t.Fatalf("期望拒绝不安全路径，实际错误: %v", err)
			}
			if _, statErr := os.Stat(outside); !os.IsNotExist(statErr) {
				t.Fatalf("不应在目标目录外写入文件，stat error: %v", statErr)
			}
		})
	}
}

func TestExtractTarGz_拒绝逃逸符号链接(t *testing.T) {
	dst := filepath.Join(t.TempDir(), "dst")
	if err := os.MkdirAll(dst, 0755); err != nil {
		t.Fatalf("创建解压目录失败: %v", err)
	}
	archivePath := writeTestTarGz(t, testTarEntry{
		name:     "link",
		typeflag: tar.TypeSymlink,
		linkname: "../../outside",
	})

	err := ExtractTarGz(archivePath, dst)
	if err == nil || !strings.Contains(err.Error(), "不安全") {
		t.Fatalf("期望拒绝不安全符号链接，实际错误: %v", err)
	}
}

func TestExtractTarGz_拒绝通过已有符号链接写出目标目录(t *testing.T) {
	tempDir := t.TempDir()
	dst := filepath.Join(tempDir, "dst")
	outside := filepath.Join(tempDir, "outside")
	if err := os.MkdirAll(outside, 0755); err != nil {
		t.Fatalf("创建目标目录外测试目录失败: %v", err)
	}
	if err := os.MkdirAll(dst, 0755); err != nil {
		t.Fatalf("创建解压目录失败: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(dst, "link")); err != nil {
		t.Skipf("当前环境无法创建符号链接: %v", err)
	}

	archivePath := writeTestTarGz(t, testTarEntry{
		name:     "link/evil.txt",
		typeflag: tar.TypeReg,
		body:     "evil",
	})

	err := ExtractTarGz(archivePath, dst)
	if err == nil || !strings.Contains(err.Error(), "符号链接") {
		t.Fatalf("期望拒绝经过符号链接组件写入，实际错误: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(outside, "evil.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("不应通过符号链接写入目标目录外，stat error: %v", statErr)
	}
}
