package models

import (
	"context"
	"slices"

	"qmediasync/internal/db"
)

// SyncFileScopeCovered 核对补齐身份后的旧位置是否已被当前许可保护。
// 调用方必须仍持有返回 sp 时的范围许可；不在许可内的位置需要释放并重新申请。
func SyncFileScopeCovered(ctx context.Context, sp *SyncPath, fileID, pickCode string) (bool, error) {
	current, scopes, err := readSyncPathScopes(ctx, db.Db, sp.ID, false, fileID, pickCode, false)
	if err != nil {
		return false, err
	}
	if current.Scope() != sp.Scope() {
		return false, nil
	}
	for _, scope := range scopes {
		if !slices.Contains(sp.scopeHeld, scope) {
			return false, nil
		}
	}
	return true, nil
}
