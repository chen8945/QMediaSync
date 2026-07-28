package backup

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"strings"
)

// innerArchiveReader 按清单读取已验证内层归档中的数据和白名单文件。
// 清单在打开时索引一次，之后每次读取都重新校验长度与散列，
// 因此提交阶段写入目标的内容与预检验证过的内容必然一致。
type innerArchiveReader struct {
	archive  *zip.ReadCloser
	entries  map[string]*zip.File
	manifest ArtifactManifest
}

func openInnerArchive(innerArchivePath string, manifest ArtifactManifest) (*innerArchiveReader, error) {
	archive, err := zip.OpenReader(innerArchivePath)
	if err != nil {
		return nil, fmt.Errorf("打开内层归档: %w", err)
	}
	entries := make(map[string]*zip.File, len(archive.File))
	for _, entry := range archive.File {
		entries[entry.Name] = entry
	}
	return &innerArchiveReader{archive: archive, entries: entries, manifest: manifest}, nil
}

func (reader *innerArchiveReader) Close() error {
	if reader.archive == nil {
		return nil
	}
	if err := reader.archive.Close(); err != nil {
		return fmt.Errorf("关闭内层归档: %w", err)
	}
	return nil
}

// manifestFile 返回清单中该路径的元数据；不在清单中的路径一律拒绝。
func (reader *innerArchiveReader) manifestFile(archivePath string) (ArtifactManifestFile, bool) {
	for _, file := range reader.manifest.Files {
		if file.Path == archivePath {
			return file, true
		}
	}
	return ArtifactManifestFile{}, false
}

// Has 判断清单中是否存在该白名单文件。
// 精确镜像依赖它：清单中不存在的目标文件必须被删除。
func (reader *innerArchiveReader) Has(archivePath string) bool {
	_, found := reader.manifestFile(archivePath)
	return found
}

// ConfigPaths 返回清单中的全部白名单配置路径。
func (reader *innerArchiveReader) ConfigPaths() []string {
	paths := make([]string, 0, len(reader.manifest.Files))
	for _, file := range reader.manifest.Files {
		if strings.HasPrefix(file.Path, "config/") {
			paths = append(paths, file.Path)
		}
	}
	return paths
}

// CopyTo 把清单文件流式写入 destination，并在结束时校验长度与散列。
func (reader *innerArchiveReader) CopyTo(archivePath string, destination io.Writer) error {
	expected, found := reader.manifestFile(archivePath)
	if !found {
		return fmt.Errorf("%w：清单中不存在该文件", ErrInvalidArtifact)
	}
	entry, exists := reader.entries[archivePath]
	if !exists {
		return fmt.Errorf("%w：内层归档缺少清单文件", ErrInvalidArtifact)
	}
	entryReader, err := entry.Open()
	if err != nil {
		return fmt.Errorf("打开内层归档条目: %w", err)
	}
	defer entryReader.Close()

	hash := sha256.New()
	limited := io.LimitReader(entryReader, expected.Size+1)
	written, err := io.Copy(io.MultiWriter(destination, hash), limited)
	if err != nil {
		return fmt.Errorf("读取内层归档条目: %w", err)
	}
	if written != expected.Size || hex.EncodeToString(hash.Sum(nil)) != expected.SHA256 {
		return fmt.Errorf("%w：内层归档条目与清单不一致", ErrInvalidArtifact)
	}
	return nil
}

// ReadFile 读取小体积清单文件（配置、密钥）的全部内容。
func (reader *innerArchiveReader) ReadFile(archivePath string) ([]byte, error) {
	expected, found := reader.manifestFile(archivePath)
	if !found {
		return nil, fmt.Errorf("%w：清单中不存在该文件", ErrInvalidArtifact)
	}
	if expected.Size > artifactMaxManifestSize {
		return nil, fmt.Errorf("%w：配置文件超出限制", ErrInvalidArtifact)
	}
	var buffer strings.Builder
	if err := reader.CopyTo(archivePath, &buffer); err != nil {
		return nil, err
	}
	return []byte(buffer.String()), nil
}

// WithFile 以流式方式消费一个清单文件，并在 consume 返回后校验长度与散列。
// 数据表按它导入：整表载入内存既不必要，也会让大工件无法恢复。
func (reader *innerArchiveReader) WithFile(archivePath string, consume func(io.Reader) error) error {
	expected, found := reader.manifestFile(archivePath)
	if !found {
		return fmt.Errorf("%w：清单中不存在该文件", ErrInvalidArtifact)
	}
	entry, exists := reader.entries[archivePath]
	if !exists {
		return fmt.Errorf("%w：内层归档缺少清单文件", ErrInvalidArtifact)
	}
	entryReader, err := entry.Open()
	if err != nil {
		return fmt.Errorf("打开内层归档条目: %w", err)
	}
	defer entryReader.Close()

	hashing := &artifactHashReader{
		reader: io.LimitReader(entryReader, expected.Size+1),
		hash:   sha256.New(),
	}
	if err := consume(hashing); err != nil {
		return err
	}
	// consume 可能提前结束；把剩余内容读完才能得到完整散列。
	if _, err := io.Copy(io.Discard, hashing); err != nil {
		return fmt.Errorf("读取内层归档条目: %w", err)
	}
	if hashing.size != expected.Size || hex.EncodeToString(hashing.hash.Sum(nil)) != expected.SHA256 {
		return fmt.Errorf("%w：内层归档条目与清单不一致", ErrInvalidArtifact)
	}
	return nil
}

// artifactConfigYAML 返回工件携带的主配置内容。
// 恢复目标由它决定；缺少 config.yaml 时回退到兼容的 config.yml。
func (reader *innerArchiveReader) artifactConfigYAML() ([]byte, error) {
	for _, candidate := range []string{"config/config.yaml", "config/config.yml"} {
		if reader.Has(candidate) {
			return reader.ReadFile(candidate)
		}
	}
	return nil, fmt.Errorf("%w：工件缺少数据库配置", ErrRestoreTargetIncompatible)
}
