package backup

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// JSONLWriter 将记录逐行编码为 JSON Lines，避免在备份期间积累整张表的数据。
type JSONLWriter struct {
	destination io.Writer
	count       int64
	size        int64
}

// NewJSONLWriter 返回一个将 JSON Lines 写入 destination 的流式写入器。
func NewJSONLWriter(destination io.Writer) *JSONLWriter {
	return &JSONLWriter{destination: destination}
}

// Write 写入一条记录，并在编码成功后递增记录数。
func (writer *JSONLWriter) Write(record any) error {
	encoded, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("编码 JSONL 记录: %w", err)
	}
	if len(encoded) > artifactMaxJSONLineSize {
		return fmt.Errorf("%w：JSONL 行超出限制", ErrInvalidArtifact)
	}
	if writer.size > artifactMaxJSONLSize-int64(len(encoded)+1) {
		return fmt.Errorf("%w：JSONL 文件超出限制", ErrInvalidArtifact)
	}
	encoded = append(encoded, '\n')
	written, err := writer.destination.Write(encoded)
	if err != nil {
		return fmt.Errorf("写入 JSONL 记录: %w", err)
	}
	if written != len(encoded) {
		return fmt.Errorf("写入 JSONL 记录: %w", io.ErrShortWrite)
	}
	writer.count++
	writer.size += int64(len(encoded))
	return nil
}

// Count 返回已经成功写入的 JSONL 记录数。
func (writer *JSONLWriter) Count() int64 {
	return writer.count
}

// WriteArtifactJSONL 原子地创建 JSONL 暂存文件，并通过 write 流式提供记录。
func WriteArtifactJSONL(destination string, write func(*JSONLWriter) error) (int64, error) {
	if destination == "" || write == nil {
		return 0, fmt.Errorf("%w：JSONL 写入参数无效", ErrInvalidArtifact)
	}
	var count int64
	err := writeArtifactAtomically(destination, func(output *os.File) error {
		buffered := bufio.NewWriterSize(output, 64*1024)
		writer := NewJSONLWriter(buffered)
		if err := write(writer); err != nil {
			return err
		}
		if err := buffered.Flush(); err != nil {
			return fmt.Errorf("刷新 JSONL 文件: %w", err)
		}
		count = writer.Count()
		return nil
	})
	if err != nil {
		return 0, err
	}
	return count, nil
}

// CollectArtifactConfigSources 收集 D1 白名单中的常规配置和日志文件。
// 配置根目录、受支持文件以及 logs 目录中的符号链接都会被拒绝，避免工件跟随目录外的内容。
func CollectArtifactConfigSources(configDir string) ([]ArtifactFileSource, error) {
	if configDir == "" {
		return nil, fmt.Errorf("%w：配置目录不能为空", ErrInvalidArtifact)
	}
	info, err := os.Lstat(configDir)
	if err != nil {
		return nil, fmt.Errorf("读取配置目录: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%w：配置目录无效", ErrInvalidArtifact)
	}

	sources := make([]ArtifactFileSource, 0, 8)
	for _, name := range []string{"config.yaml", "config.yml", "encryption.key", "server.crt", "server.key"} {
		archivePath := "config/" + name
		sourcePath := filepath.Join(configDir, name)
		required := name == "encryption.key"
		if err := appendArtifactConfigFile(&sources, archivePath, sourcePath, required); err != nil {
			return nil, err
		}
	}

	logsRoot := filepath.Join(configDir, "logs")
	if err := collectArtifactLogSources(&sources, logsRoot); err != nil {
		return nil, err
	}
	sort.Slice(sources, func(i int, j int) bool {
		return sources[i].ArchivePath < sources[j].ArchivePath
	})
	return sources, nil
}

func appendArtifactConfigFile(sources *[]ArtifactFileSource, archivePath string, sourcePath string, required bool) error {
	info, err := os.Lstat(sourcePath)
	if os.IsNotExist(err) {
		if required {
			return fmt.Errorf("%w：缺少必需的配置文件", ErrInvalidArtifact)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("读取配置文件: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w：配置白名单文件必须是常规文件", ErrInvalidArtifact)
	}
	*sources = append(*sources, ArtifactFileSource{ArchivePath: archivePath, SourcePath: sourcePath})
	return nil
}

func collectArtifactLogSources(sources *[]ArtifactFileSource, logsRoot string) error {
	info, err := os.Lstat(logsRoot)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("读取日志目录: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w：日志目录无效", ErrInvalidArtifact)
	}

	return filepath.WalkDir(logsRoot, func(currentPath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return fmt.Errorf("遍历日志目录: %w", walkErr)
		}
		if currentPath == logsRoot {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w：日志目录包含符号链接", ErrInvalidArtifact)
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("读取日志文件: %w", err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%w：日志目录包含非常规文件", ErrInvalidArtifact)
		}
		relative, err := filepath.Rel(logsRoot, currentPath)
		if err != nil || relative == "." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
			return fmt.Errorf("%w：日志路径无效", ErrInvalidArtifact)
		}
		archivePath := "config/logs/" + filepath.ToSlash(relative)
		if err := validateArtifactArchivePath(archivePath); err != nil {
			return err
		}
		*sources = append(*sources, ArtifactFileSource{ArchivePath: archivePath, SourcePath: currentPath})
		return nil
	})
}
