package backup

import (
	"archive/zip"
	"compress/flate"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// MaxArchiveSize 是导出、上传和恢复共用的 ZIP 文件上限。
const MaxArchiveSize int64 = 1 << 30

const (
	maxBackupExpandedSize int64 = 16 << 30
	maxBackupRowSize      int64 = 64 << 20
	maxBackupManifestSize int64 = 8 << 20
	maxBackupMetadataRead int64 = 8 << 20
	maxBackupEntries            = 4096
)

type backupArchiveLimits struct {
	compressed int64
	expanded   int64
	manifest   int64
	metadata   int64
	entries    int
}

type backupMetadataReader struct {
	io.ReaderAt
	remaining int64
	parsing   bool
	exceeded  bool
}

func (reader *backupMetadataReader) ReadAt(content []byte, offset int64) (int, error) {
	if reader.parsing {
		if int64(len(content)) > reader.remaining {
			reader.exceeded = true
			return 0, ErrArchiveLimit
		}
		reader.remaining -= int64(len(content))
	}
	return reader.ReaderAt.ReadAt(content, offset)
}

func extractBackupArchive(src, dst string) error {
	return extractBackupArchiveWithLimits(src, dst, backupArchiveLimits{
		compressed: MaxArchiveSize, expanded: maxBackupExpandedSize,
		manifest: maxBackupManifestSize, metadata: maxBackupMetadataRead, entries: maxBackupEntries,
	})
}

func extractBackupArchiveWithLimits(src, dst string, limits backupArchiveLimits) (err error) {
	file, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%w：备份不是普通文件", ErrArchiveInvalid)
	}
	if info.Size() > limits.compressed {
		return fmt.Errorf("%w：ZIP 文件过大", ErrArchiveLimit)
	}
	// ponytail: stdlib 仍可能按声明数量预分配（受 1 GiB 输入限制）；更严格内存隔离需单独评估。
	metadata := &backupMetadataReader{ReaderAt: file, remaining: limits.metadata, parsing: true}
	archive, err := zip.NewReader(metadata, info.Size())
	metadata.parsing = false
	if metadata.exceeded {
		return fmt.Errorf("%w：ZIP 目录解析读取量过大", ErrArchiveLimit)
	}
	if err != nil {
		if _, pathError := errors.AsType[*os.PathError](err); pathError {
			return err
		}
		return fmt.Errorf("%w：ZIP 结构无效", ErrArchiveInvalid)
	}
	if len(archive.File) > limits.entries {
		return fmt.Errorf("%w：ZIP 条目过多", ErrArchiveLimit)
	}
	// 先检查全部条目，避免遇到重复路径或声明超限时已写出部分内容。
	names := make([]string, len(archive.File))
	seen := make(map[string]bool, len(archive.File))
	var expanded uint64
	for i, entry := range archive.File {
		name, err := backupArchivePath(entry.Name)
		if err != nil {
			return err
		}
		names[i] = name
		key := strings.ToLower(name)
		if _, exists := seen[key]; exists {
			return fmt.Errorf("%w：ZIP 条目路径重复", ErrArchiveInvalid)
		}
		mode := entry.Mode()
		if kind := mode & os.ModeType; kind != 0 && kind != os.ModeDir {
			return fmt.Errorf("%w：ZIP 包含特殊文件", ErrArchiveInvalid)
		}
		// 清单只能位于根目录，不能因包装目录或大小写别名退回旧格式兼容。
		if strings.EqualFold(strings.TrimRight(path.Base(name), ". "), logicalManifestFile) &&
			(entry.Name != logicalManifestFile || !mode.IsRegular()) {
			return fmt.Errorf("%w：备份清单必须是根目录的 manifest.json 普通文件", ErrArchiveInvalid)
		}
		seen[key] = mode.IsDir()
		if entry.Flags&1 != 0 || (entry.Method != zip.Store && entry.Method != zip.Deflate) {
			return fmt.Errorf("%w：ZIP 压缩方式不受支持", ErrArchiveUnsupported)
		}
		if mode.IsDir() && entry.UncompressedSize64 != 0 {
			return fmt.Errorf("%w：ZIP 目录包含数据", ErrArchiveInvalid)
		}
		if entry.UncompressedSize64 > uint64(limits.expanded)-expanded ||
			(path.Base(name) == logicalManifestFile && entry.UncompressedSize64 > uint64(limits.manifest)) {
			return fmt.Errorf("%w：ZIP 展开内容过大", ErrArchiveLimit)
		}
		expanded += entry.UncompressedSize64
	}
	for name := range seen {
		for parent := path.Dir(name); parent != "."; parent = path.Dir(parent) {
			if isDir, exists := seen[parent]; exists && !isDir {
				return fmt.Errorf("%w：ZIP 文件与目录路径冲突", ErrArchiveInvalid)
			}
		}
	}
	info, err = os.Lstat(dst)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w：恢复目录无效", ErrArchiveInvalid)
	}
	root, err := os.OpenRoot(dst)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	remaining := limits.expanded
	for i, entry := range archive.File {
		name := names[i]
		directory := path.Dir(name)
		if entry.Mode().IsDir() {
			directory = name
		}
		if err := ensureBackupArchiveDir(root, directory); err != nil {
			return err
		}
		if entry.Mode().IsDir() {
			continue
		}
		limit := remaining
		if path.Base(name) == logicalManifestFile {
			limit = min(limit, limits.manifest)
		}
		written, err := extractBackupEntry(root, entry, name, limit)
		if err != nil {
			return err
		}
		remaining -= written
	}
	return nil
}

func backupArchivePath(name string) (string, error) {
	// 旧 Windows 归档可能使用反斜杠；统一后再检查重复与目录碰撞。
	name = strings.ReplaceAll(name, "\\", "/")
	name = strings.TrimSuffix(name, "/")
	if name == "" || strings.ContainsAny(name, ":\x00") || !fs.ValidPath(name) || !filepath.IsLocal(name) {
		return "", fmt.Errorf("%w：ZIP 路径无效", ErrArchiveInvalid)
	}
	return name, nil
}

func ensureBackupArchiveDir(root *os.Root, name string) error {
	if name == "." {
		return nil
	}
	current := ""
	for _, component := range strings.Split(name, "/") {
		current = path.Join(current, component)
		if err := root.Mkdir(current, 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
		info, err := root.Lstat(current)
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w：恢复路径包含特殊文件", ErrArchiveInvalid)
		}
		if err := root.Chmod(current, 0700); err != nil {
			return err
		}
	}
	return nil
}

func extractBackupEntry(root *os.Root, entry *zip.File, name string, limit int64) (written int64, err error) {
	source, err := entry.Open()
	if err != nil {
		if _, pathError := errors.AsType[*os.PathError](err); pathError {
			return 0, err
		}
		return 0, fmt.Errorf("%w：无法读取 ZIP 条目", ErrArchiveInvalid)
	}
	defer func() { err = errors.Join(err, source.Close()) }()
	destination, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return 0, err
	}
	defer func() { err = errors.Join(err, destination.Close()) }()
	// 实际读取再限流，不把 ZIP 中的声明大小当作磁盘写入保证。
	written, err = io.Copy(destination, io.LimitReader(source, limit))
	if err != nil {
		_, corrupt := errors.AsType[flate.CorruptInputError](err)
		if corrupt || errors.Is(err, zip.ErrChecksum) || errors.Is(err, zip.ErrFormat) || errors.Is(err, io.ErrUnexpectedEOF) {
			return written, fmt.Errorf("%w：ZIP 内容损坏", ErrArchiveInvalid)
		}
		return written, err
	}
	var extra [1]byte
	n, readErr := io.ReadFull(source, extra[:])
	if n != 0 {
		return written, fmt.Errorf("%w：ZIP 实际展开内容过大", ErrArchiveLimit)
	}
	if !errors.Is(readErr, io.EOF) {
		if _, pathError := errors.AsType[*os.PathError](readErr); pathError {
			return written, readErr
		}
		return written, fmt.Errorf("%w：ZIP 内容不完整", ErrArchiveInvalid)
	}
	return written, nil
}

// readBackupRows 保留 JSON decoder 的记录边界，限制每次解码的读入量及内存增长。
func readBackupRows(reader io.Reader, limit int64, consume func(json.RawMessage) error) error {
	limited := &io.LimitedReader{R: reader}
	decoder := json.NewDecoder(limited)
	var read int64
	for {
		start := decoder.InputOffset()
		// Decoder 可能已预读下一条记录；这部分也要计入本轮上限。
		limited.N = limit + 1 - (read - start)
		available := limited.N
		var raw json.RawMessage
		err := decoder.Decode(&raw)
		read += available - limited.N
		if decoder.InputOffset()-start > limit ||
			(limited.N == 0 && (errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF))) {
			return fmt.Errorf("%w：JSON 记录过大", ErrArchiveLimit)
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			if _, syntax := errors.AsType[*json.SyntaxError](err); syntax || errors.Is(err, io.ErrUnexpectedEOF) {
				return fmt.Errorf("%w：JSON 记录无效", ErrArchiveInvalid)
			}
			return err
		}
		if err := consume(raw); err != nil {
			return err
		}
	}
}

func readLimitedBackupFile(name string, limit int64) (content []byte, err error) {
	file, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	content, err = io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(content)) > limit {
		return nil, fmt.Errorf("%w：备份清单过大", ErrArchiveLimit)
	}
	return content, nil
}

func validateLogicalFiles(dir string, tables []logicalTable) error {
	files := map[string]bool{logicalManifestFile: false}
	for _, table := range tables {
		files[table.File] = false
		for parent := path.Dir(table.File); parent != "."; parent = path.Dir(parent) {
			files[parent] = true
		}
	}
	remaining, count := maxBackupExpandedSize, 0
	err := filepath.WalkDir(dir, func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if name == dir {
			return nil
		}
		count++
		if count > maxBackupEntries {
			return fmt.Errorf("%w：备份文件过多", ErrArchiveLimit)
		}
		relative, err := filepath.Rel(dir, name)
		if err != nil {
			return err
		}
		isDir, exists := files[filepath.ToSlash(relative)]
		if !exists || entry.IsDir() != isDir || (entry.Type() != 0 && !entry.IsDir()) {
			return fmt.Errorf("%w：备份包含清单以外的文件或特殊条目", ErrArchiveInvalid)
		}
		delete(files, filepath.ToSlash(relative))
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Size() > remaining {
			return fmt.Errorf("%w：备份展开内容过大", ErrArchiveLimit)
		}
		remaining -= info.Size()
		return nil
	})
	if err != nil {
		return err
	}
	if len(files) != 0 {
		return fmt.Errorf("%w：备份缺少清单声明的文件", ErrArchiveInvalid)
	}
	return nil
}
