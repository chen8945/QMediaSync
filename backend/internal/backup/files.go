package backup

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"qmediasync/internal/helpers"
)

var (
	ErrBackupFileNameInvalid = errors.New("备份文件名无效")
	ErrBackupFileUnavailable = errors.New("备份文件不可用")
)

// BackupFile 只包含文件系统元数据；出现在列表中不代表归档已通过恢复校验。
type BackupFile struct {
	FileName   string `json:"file_name"`
	FileSize   int64  `json:"file_size"`
	ModifiedAt int64  `json:"modified_at"`
}

// BackupDirectory 返回当前实例实际使用的备份目录。
func BackupDirectory() string {
	return filepath.Join(helpers.ConfigDir, "backups")
}

func validBackupFileName(name string) bool {
	return name != "" && filepath.IsLocal(name) && !strings.ContainsAny(name, "/\\:\x00") &&
		strings.EqualFold(filepath.Ext(name), ".zip")
}

func backupFileName(filePath string) (string, error) {
	if filePath == "" {
		return "", ErrBackupFileUnavailable
	}
	name, err := filepath.Rel(BackupDirectory(), filePath)
	if err != nil || !validBackupFileName(name) {
		return "", ErrBackupFileUnavailable
	}
	return name, nil
}

func openBackupDirectory() (*os.Root, error) {
	config, err := os.OpenRoot(helpers.ConfigDir)
	if err != nil {
		return nil, err
	}
	defer config.Close()
	return openBackupSubdirectory(config, "backups")
}

func openBackupSubdirectory(parent *os.Root, name string) (*os.Root, error) {
	info, err := parent.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, ErrBackupFileUnavailable
	}
	root, err := parent.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		root.Close()
		return nil, ErrBackupFileUnavailable
	}
	return root, nil
}

// ListBackupFiles 仅列举根目录普通 ZIP 的元数据，不解析、解压或计算归档摘要。
func ListBackupFiles() ([]BackupFile, error) {
	files := make([]BackupFile, 0)
	root, err := openBackupDirectory()
	if errors.Is(err, os.ErrNotExist) {
		return files, nil
	}
	if err != nil {
		return nil, err
	}
	defer root.Close()
	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if !validBackupFileName(entry.Name()) || entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		info, err := root.Lstat(entry.Name())
		if errors.Is(err, os.ErrNotExist) {
			continue // 列举期间被正常清理的文件不使整页失败。
		}
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() {
			continue
		}
		files = append(files, BackupFile{FileName: entry.Name(), FileSize: info.Size(), ModifiedAt: info.ModTime().Unix()})
	}
	sort.Slice(files, func(i, j int) bool {
		if files[i].ModifiedAt == files[j].ModifiedAt {
			return files[i].FileName < files[j].FileName
		}
		return files[i].ModifiedAt > files[j].ModifiedAt
	})
	return files, nil
}

// ResolveBackupFile 只允许当前备份根目录中的普通 ZIP，不接受路径或符号链接。
func ResolveBackupFile(fileName string) (string, error) {
	if !validBackupFileName(fileName) {
		return "", ErrBackupFileNameInvalid
	}
	root, err := openBackupDirectory()
	if err != nil {
		return "", err
	}
	defer root.Close()
	info, err := root.Lstat(fileName)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", ErrBackupFileUnavailable
	}
	return filepath.Join(BackupDirectory(), fileName), nil
}

// BackupFileAvailability 将存在性与执行历史分开；读取异常不能当作文件已丢失。
func BackupFileAvailability(filePath string) string {
	name, err := backupFileName(filePath)
	if err != nil {
		return "unavailable"
	}
	root, err := openBackupDirectory()
	if err != nil {
		return "unavailable"
	}
	defer root.Close()
	info, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return "missing"
	}
	if err != nil || !info.Mode().IsRegular() {
		return "unavailable"
	}
	return "available"
}

// OpenBackupFile 重新核验并打开受管归档，调用方通过返回的文件句柄读取，避免随后按路径跟随替换的链接。
func OpenBackupFile(filePath string) (*os.File, error) {
	name, err := backupFileName(filePath)
	if err != nil {
		return nil, err
	}
	root, err := openBackupDirectory()
	if err != nil {
		return nil, err
	}
	defer root.Close()
	return openRegularBackupFile(root, name)
}

func openRegularBackupFile(root *os.Root, name string) (*os.File, error) {
	before, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() {
		return nil, ErrBackupFileUnavailable
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(before, opened) {
		file.Close()
		return nil, ErrBackupFileUnavailable
	}
	after, err := root.Lstat(name)
	if err != nil || !after.Mode().IsRegular() || !os.SameFile(opened, after) ||
		before.Size() != opened.Size() || !before.ModTime().Equal(opened.ModTime()) {
		file.Close()
		return nil, ErrBackupFileUnavailable
	}
	return file, nil
}

func openArchiveSource(filePath string) (*os.File, error) {
	// 正式归档和本轮私有发布临时文件都从受管目录句柄打开，防止目录被链接替换。
	if filepath.Clean(filepath.Dir(filePath)) == filepath.Clean(BackupDirectory()) {
		root, err := openBackupDirectory()
		if err != nil {
			return nil, err
		}
		defer root.Close()
		return openRegularBackupFile(root, filepath.Base(filePath))
	}
	root, err := os.OpenRoot(filepath.Dir(filePath))
	if err != nil {
		return nil, err
	}
	defer root.Close()
	return openRegularBackupFile(root, filepath.Base(filePath))
}

func restoreUploadedArchive(tempPath string) error {
	if err := helpers.EnsurePrivateDir(helpers.ConfigDir, "backups"); err != nil {
		return err
	}
	stagedPath, err := stageUploadedBackup(tempPath)
	if err != nil {
		return err
	}
	defer os.Remove(stagedPath)
	return restoreArchive(stagedPath, true)
}

func stageUploadedBackup(tempPath string) (stagedPath string, err error) {
	if filepath.Clean(filepath.Dir(tempPath)) != filepath.Join(BackupDirectory(), "temp") {
		return "", ErrBackupFileUnavailable
	}
	root, err := openBackupDirectory()
	if err != nil {
		return "", err
	}
	defer root.Close()
	temp, err := openBackupSubdirectory(root, "temp")
	if err != nil {
		return "", err
	}
	defer temp.Close()
	source, err := openRegularBackupFile(temp, filepath.Base(tempPath))
	if err != nil {
		return "", err
	}
	defer source.Close()
	name := ".backup-publish-" + rand.Text() + ".part"
	destination, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return "", err
	}
	defer func() {
		if err != nil {
			root.Remove(name)
		}
	}()
	written, copyErr := io.Copy(destination, io.LimitReader(source, MaxArchiveSize+1))
	err = errors.Join(copyErr, destination.Close())
	if written > MaxArchiveSize {
		err = errors.Join(err, ErrArchiveLimit)
	}
	if err != nil {
		return "", err
	}
	return filepath.Join(BackupDirectory(), name), nil
}

func publishUploadedBackup(stagedPath string) (string, error) {
	fileName := fmt.Sprintf("backup_upload_%s_%s.zip", time.Now().Format("20060102_150405"), rand.Text())
	filePath := filepath.Join(BackupDirectory(), fileName)
	if err := helpers.SyncAndPublishFileNoReplace(stagedPath, filePath); err != nil {
		return "", fmt.Errorf("保存上传备份失败：%w", err)
	}
	return filePath, nil
}
