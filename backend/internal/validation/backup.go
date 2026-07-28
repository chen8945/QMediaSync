package validation

import (
	"fmt"
	"unicode"
	"unicode/utf8"
)

const MinBackupPasswordLength = 10

// BackupPassword 校验备份工件密码；空值表示不使用工件密码。
func BackupPassword(field string, password string) error {
	if password == "" {
		return nil
	}
	if utf8.RuneCountInString(password) < MinBackupPasswordLength {
		return New(field, fmt.Sprintf("长度不能小于 %d 个字符", MinBackupPasswordLength))
	}

	var hasUpper, hasLower, hasDigit bool
	for _, item := range password {
		if unicode.IsSpace(item) {
			return New(field, "不能包含空白字符")
		}
		switch {
		case item >= 'A' && item <= 'Z':
			hasUpper = true
		case item >= 'a' && item <= 'z':
			hasLower = true
		case item >= '0' && item <= '9':
			hasDigit = true
		}
	}
	if !hasUpper || !hasLower || !hasDigit {
		return New(field, "必须包含大写字母、小写字母和数字")
	}
	return nil
}
