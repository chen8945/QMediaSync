package backup

import "errors"

var (
	ErrArchiveInvalid     = errors.New("备份文件损坏或内容不完整")
	ErrArchiveUnsupported = errors.New("备份格式或数据库版本不兼容")
	ErrArchiveLimit       = errors.New("备份超过支持的资源限制")
	ErrRestoreKeyMismatch = errors.New("当前实例密钥无法解密备份中的两步验证数据")
)

// FailureCode 只将明确分类的错误映射为稳定机器值，不公开底层错误内容。
func FailureCode(taskType string, err error) string {
	if err == nil {
		return ""
	}
	switch {
	case errors.Is(err, ErrRestoreKeyMismatch):
		return "RESTORE_KEY_MISMATCH"
	case errors.Is(err, ErrArchiveUnsupported):
		return "BACKUP_ARCHIVE_UNSUPPORTED"
	case errors.Is(err, ErrArchiveLimit):
		return "BACKUP_ARCHIVE_LIMIT"
	case errors.Is(err, ErrArchiveInvalid):
		return "BACKUP_ARCHIVE_INVALID"
	case taskType == "restore":
		return "RESTORE_FAILED"
	default:
		return "BACKUP_FAILED"
	}
}

func failureMessage(taskType string, err error) string {
	message := taskFailureMessage(taskType)
	switch FailureCode(taskType, err) {
	case "RESTORE_KEY_MISMATCH":
		message = "当前实例密钥无法解密备份中的两步验证数据，请使用备份所属实例的 encryption.key。"
	case "BACKUP_ARCHIVE_UNSUPPORTED":
		message = "备份格式或数据库版本不兼容，请使用兼容版本的备份。"
	case "BACKUP_ARCHIVE_LIMIT":
		message = "备份超过支持的资源限制，请检查备份文件大小和内容。"
	case "BACKUP_ARCHIVE_INVALID":
		message = "备份文件损坏或内容不完整，请检查备份文件。"
	}
	return message
}
