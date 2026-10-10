package database

import (
	"fmt"
	"unicode"

	"github.com/jackc/pgx/v5"
)

const maxPostgresIdentifierBytes = 63

// QuotePostgresIdentifier 校验并引用 PostgreSQL 标识符。
func QuotePostgresIdentifier(name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("PostgreSQL 标识符不能为空")
	}
	if len(name) > maxPostgresIdentifierBytes {
		return "", fmt.Errorf("PostgreSQL 标识符长度不能超过 %d 字节", maxPostgresIdentifierBytes)
	}
	for _, r := range name {
		if r == 0 || unicode.IsControl(r) || unicode.IsSpace(r) {
			return "", fmt.Errorf("PostgreSQL 标识符不能包含空白或控制字符")
		}
	}
	return pgx.Identifier{name}.Sanitize(), nil
}
