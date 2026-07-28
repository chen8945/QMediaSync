package backup

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"qmediasync/internal/models"
)

// CreateInnerArchive 将已暂存的 JSONL 和白名单文件写入 Deflate 内层 ZIP。
func CreateInnerArchive(destination string, manifest ArtifactManifest, sources []ArtifactFileSource) (ArtifactManifest, error) {
	if err := validateManifestMetadata(manifest); err != nil {
		return ArtifactManifest{}, err
	}
	if len(sources) == 0 {
		return ArtifactManifest{}, fmt.Errorf("%w：没有待归档文件", ErrInvalidArtifact)
	}

	sortedSources := append([]ArtifactFileSource(nil), sources...)
	sort.Slice(sortedSources, func(i int, j int) bool {
		return sortedSources[i].ArchivePath < sortedSources[j].ArchivePath
	})
	manifest.Files = make([]ArtifactManifestFile, 0, len(sortedSources))
	seenPaths := make(map[string]struct{}, len(sortedSources))
	var totalSize int64

	err := writeArtifactAtomically(destination, func(output *os.File) error {
		writer := zip.NewWriter(output)
		for _, source := range sortedSources {
			if _, exists := seenPaths[source.ArchivePath]; exists {
				writer.Close()
				return fmt.Errorf("%w：归档文件重复", ErrInvalidArtifact)
			}
			seenPaths[source.ArchivePath] = struct{}{}
			if err := validateArtifactSource(source); err != nil {
				writer.Close()
				return err
			}
			if strings.HasPrefix(source.ArchivePath, "data/") {
				recordCount, err := countArtifactJSONLFile(source.SourcePath)
				if err != nil {
					writer.Close()
					return err
				}
				if recordCount != source.RecordCount {
					writer.Close()
					return fmt.Errorf("%w：JSONL 记录数不匹配", ErrInvalidArtifact)
				}
			}

			metadata, err := writeInnerArchiveFile(writer, source)
			if err != nil {
				writer.Close()
				return err
			}
			if totalSize > artifactMaxInnerSize-metadata.Size {
				writer.Close()
				return fmt.Errorf("%w：内层归档超出限制", ErrInvalidArtifact)
			}
			totalSize += metadata.Size
			manifest.Files = append(manifest.Files, metadata)
		}
		if err := validateManifestFiles(manifest); err != nil {
			writer.Close()
			return err
		}

		manifestBytes, err := json.Marshal(manifest)
		if err != nil {
			writer.Close()
			return fmt.Errorf("编码工件清单: %w", err)
		}
		if len(manifestBytes) > artifactMaxManifestSize {
			writer.Close()
			return fmt.Errorf("%w：工件清单超出限制", ErrInvalidArtifact)
		}
		if err := writeZipEntry(writer, artifactManifestPath, zip.Deflate, strings.NewReader(string(manifestBytes))); err != nil {
			writer.Close()
			return err
		}
		if err := writer.Close(); err != nil {
			return fmt.Errorf("关闭内层归档: %w", err)
		}
		return nil
	})
	if err != nil {
		return ArtifactManifest{}, err
	}
	return manifest, nil
}

func validateManifestMetadata(manifest ArtifactManifest) error {
	if manifest.FormatVersion != ArtifactFormatVersion || !isArtifactID(manifest.ArtifactID) ||
		manifest.ApplicationVersion == "" || manifest.SchemaVersion < 0 || !isSupportedEngine(manifest.SourceEngine) ||
		manifest.TableCatalogVersion != ArtifactTableCatalogVersion {
		return fmt.Errorf("%w：清单兼容信息无效", ErrInvalidArtifact)
	}
	return nil
}

func validateArtifactSource(source ArtifactFileSource) error {
	if err := validateArtifactArchivePath(source.ArchivePath); err != nil {
		return err
	}
	if strings.HasPrefix(source.ArchivePath, "data/") {
		if !isExpectedArtifactDataPath(source.ArchivePath) {
			return fmt.Errorf("%w：归档数据路径不在表目录中", ErrInvalidArtifact)
		}
	} else if !isAllowedConfigPath(source.ArchivePath) {
		return fmt.Errorf("%w：归档路径不在白名单中", ErrInvalidArtifact)
	}
	if source.RecordCount < 0 {
		return fmt.Errorf("%w：记录数无效", ErrInvalidArtifact)
	}
	info, err := os.Lstat(source.SourcePath)
	if err != nil {
		return fmt.Errorf("读取归档源文件: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w：归档源文件必须是常规文件", ErrInvalidArtifact)
	}
	if info.Size() < 0 || info.Size() > artifactMaxInnerSize ||
		(strings.HasPrefix(source.ArchivePath, "data/") && info.Size() > artifactMaxJSONLSize) {
		return fmt.Errorf("%w：归档源文件超出限制", ErrInvalidArtifact)
	}
	return nil
}

func countArtifactJSONLFile(sourcePath string) (int64, error) {
	input, err := os.Open(sourcePath)
	if err != nil {
		return 0, fmt.Errorf("打开 JSONL 源文件: %w", err)
	}
	defer input.Close()
	return verifyJSONLines(input)
}

func validateManifestFiles(manifest ArtifactManifest) error {
	header := ArtifactHeader{
		FormatVersion:       manifest.FormatVersion,
		ArtifactID:          manifest.ArtifactID,
		ApplicationVersion:  manifest.ApplicationVersion,
		SchemaVersion:       manifest.SchemaVersion,
		SourceEngine:        manifest.SourceEngine,
		TableCatalogVersion: manifest.TableCatalogVersion,
	}
	return manifest.validateAgainstHeader(header)
}

func isExpectedArtifactDataPath(archivePath string) bool {
	for _, entry := range models.RegularBackupRestoreTableCatalog() {
		if archivePath == "data/"+entry.ID+".jsonl" {
			return true
		}
	}
	return false
}

func writeInnerArchiveFile(writer *zip.Writer, source ArtifactFileSource) (ArtifactManifestFile, error) {
	input, err := os.Open(source.SourcePath)
	if err != nil {
		return ArtifactManifestFile{}, fmt.Errorf("打开归档源文件: %w", err)
	}
	defer input.Close()

	header := &zip.FileHeader{Name: source.ArchivePath, Method: zip.Deflate}
	header.SetMode(0o600)
	output, err := writer.CreateHeader(header)
	if err != nil {
		return ArtifactManifestFile{}, fmt.Errorf("创建内层归档条目: %w", err)
	}
	hash := sha256.New()
	size, err := io.Copy(io.MultiWriter(output, hash), input)
	if err != nil {
		return ArtifactManifestFile{}, fmt.Errorf("写入内层归档条目: %w", err)
	}
	return ArtifactManifestFile{
		Path:        source.ArchivePath,
		Size:        size,
		SHA256:      hex.EncodeToString(hash.Sum(nil)),
		RecordCount: source.RecordCount,
	}, nil
}

func writeZipEntry(writer *zip.Writer, name string, method uint16, content io.Reader) error {
	header := &zip.FileHeader{Name: name, Method: method}
	header.SetMode(0o600)
	entry, err := writer.CreateHeader(header)
	if err != nil {
		return fmt.Errorf("创建 ZIP 条目: %w", err)
	}
	if _, err := io.Copy(entry, content); err != nil {
		return fmt.Errorf("写入 ZIP 条目: %w", err)
	}
	return nil
}

func artifactSourcePath(configDir string, archivePath string) string {
	return filepath.Join(configDir, filepath.FromSlash(strings.TrimPrefix(archivePath, "config/")))
}
