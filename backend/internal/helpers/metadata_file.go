package helpers

import (
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// 发布锁按解析父目录后的目标定位，避免符号链接别名绕过同一目标的保护。
var metadataPublishLocks = struct {
	sync.Mutex
	entries map[string]*metadataPublishLock
}{entries: make(map[string]*metadataPublishLock)}

type metadataPublishLock struct {
	mu    sync.Mutex
	users int
}

func lockMetadataPublish(path string) (func(), error) {
	parent, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	target, err := filepath.Abs(filepath.Join(parent, filepath.Base(path)))
	if err != nil {
		return nil, err
	}
	metadataPublishLocks.Lock()
	entry := metadataPublishLocks.entries[target]
	if entry == nil {
		entry = &metadataPublishLock{}
		metadataPublishLocks.entries[target] = entry
	}
	entry.users++
	metadataPublishLocks.Unlock()
	entry.mu.Lock()
	return func() {
		entry.mu.Unlock()
		metadataPublishLocks.Lock()
		entry.users--
		if entry.users == 0 {
			delete(metadataPublishLocks.entries, target)
		}
		metadataPublishLocks.Unlock()
	}, nil
}

// MetadataFingerprint 记录文件内容和修改时间；符号链接和目录不能作为替换目标。
func MetadataFingerprint(path string) (string, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !before.Mode().IsRegular() {
		return "", fmt.Errorf("元数据目标不是普通文件：%s", path)
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !os.SameFile(before, opened) || before.Size() != opened.Size() || !before.ModTime().Equal(opened.ModTime()) {
		return "", fmt.Errorf("元数据文件打开时发生变化：%s", path)
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	after, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !os.SameFile(before, after) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return "", fmt.Errorf("元数据文件读取时发生变化：%s", path)
	}
	return fmt.Sprintf("%d:%d:%x", after.Size(), after.ModTime().UnixNano(), h.Sum(nil)), nil
}

// WriteMetadataFile 写完并检查临时文件后才发布；baseline 为空时只创建缺失文件。
// beforePublish 在文件通过校验后保存恢复信息，也可复核复制源是否变化。
func WriteMetadataFile(path, baseline string, size int64, sha1Value, md5Value string, mtime int64, write func(io.Writer) error, beforePublish func(string) error) error {
	if err := CreateDirWithPerm(filepath.Dir(path), 0777); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".qms-metadata-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	h1, hm, h256 := sha1.New(), md5.New(), sha256.New()
	if err := write(io.MultiWriter(f, h1, hm, h256)); err != nil {
		return err
	}
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if size >= 0 && info.Size() != size {
		return fmt.Errorf("元数据大小不符：期望 %d，实际 %d", size, info.Size())
	}
	if sha1Value != "" && !strings.EqualFold(sha1Value, fmt.Sprintf("%x", h1.Sum(nil))) {
		return fmt.Errorf("元数据 SHA1 不符")
	}
	if md5Value != "" && !strings.EqualFold(md5Value, fmt.Sprintf("%x", hm.Sum(nil))) {
		return fmt.Errorf("元数据 MD5 不符")
	}
	if err := f.Chmod(0777); err != nil {
		return err
	}
	if mtime > 0 {
		t := time.Unix(mtime, 0)
		if err := os.Chtimes(f.Name(), t, t); err != nil {
			return err
		}
	}
	if err := f.Close(); err != nil {
		return err
	}
	if beforePublish != nil {
		if err := beforePublish(fmt.Sprintf("%x", h256.Sum(nil))); err != nil {
			return err
		}
	}
	unlock, err := lockMetadataPublish(path)
	if err != nil {
		return err
	}
	defer unlock()
	if baseline == "" {
		return publishMissingMetadata(f.Name(), path, os.Link, renameMetadataNoReplace)
	}
	current, err := MetadataFingerprint(path)
	if err != nil {
		return err
	}
	if current != baseline {
		return fmt.Errorf("本地元数据已变化，保留当前文件：%s", path)
	}
	return os.Rename(f.Name(), path)
}

// publishMissingMetadata 仅在硬链接能力缺失时尝试原生非覆盖重命名，始终发布完整文件。
func publishMissingMetadata(source, target string, link, renameNoReplace func(string, string) error) error {
	linkErr := link(source, target)
	if linkErr == nil || !metadataLinkUnsupported(source, linkErr) {
		return linkErr
	}
	if err := renameNoReplace(source, target); err != nil {
		return fmt.Errorf("元数据无法非覆盖发布（硬链接失败：%v；原生非覆盖重命名失败）：%w", linkErr, err)
	}
	return nil
}

// CopyMetadataFile 允许跟随源符号链接，发布前复核原路径、文件身份和内容；目标仍须是普通文件。
func CopyMetadataFile(source, target, baseline string, size, mtime int64, beforePublish func(string) error) error {
	source, err := filepath.Abs(source)
	if err != nil {
		return err
	}
	entry, err := os.Lstat(source)
	if err != nil {
		return err
	}
	resolved, err := filepath.EvalSymlinks(source)
	if err != nil {
		return err
	}
	sourceInfo, err := os.Stat(source)
	if err != nil {
		return err
	}
	if !sourceInfo.Mode().IsRegular() {
		return fmt.Errorf("元数据源不是普通文件：%s", source)
	}
	f, err := os.Open(resolved)
	if err != nil {
		return err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(sourceInfo, opened) || sourceInfo.Size() != opened.Size() || !sourceInfo.ModTime().Equal(opened.ModTime()) {
		return fmt.Errorf("复制前源文件已变化：%s", source)
	}
	return WriteMetadataFile(target, baseline, size, "", "", mtime, func(w io.Writer) error { _, err := io.Copy(w, f); return err }, func(hash string) error {
		if beforePublish != nil {
			if err := beforePublish(hash); err != nil {
				return err
			}
		}
		currentPath, err := filepath.EvalSymlinks(source)
		if err != nil {
			return err
		}
		currentEntry, err := os.Lstat(source)
		if err != nil {
			return err
		}
		currentInfo, err := os.Stat(source)
		if err != nil {
			return err
		}
		if currentPath != resolved || !os.SameFile(entry, currentEntry) || !os.SameFile(opened, currentInfo) ||
			opened.Size() != currentInfo.Size() || !opened.ModTime().Equal(currentInfo.ModTime()) {
			return fmt.Errorf("复制期间源文件已变化：%s", source)
		}
		current, err := MetadataFingerprint(resolved)
		if err != nil {
			return err
		}
		if current != fmt.Sprintf("%d:%d:%s", opened.Size(), opened.ModTime().UnixNano(), hash) {
			return fmt.Errorf("复制期间源文件已变化：%s", source)
		}
		return nil
	})
}
