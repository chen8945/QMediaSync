package models

import (
	"context"
	"fmt"

	"gorm.io/gorm"
)

// AdminRecoveryResult 仅向本地恢复命令交付已提交的结果，禁止整体写入日志。
type AdminRecoveryResult struct {
	UserID   uint
	Username string
	Password string `json:"-"`
	Deleted  bool
}

// RecoverAdmin 在停服后的独占连接中原子重置或删除唯一管理员。
func RecoverAdmin(ctx context.Context, conn *gorm.DB, deleteAdmin bool) (*AdminRecoveryResult, error) {
	conn = conn.WithContext(ctx)
	for _, table := range []string{(User{}).TableName(), (UserSession{}).TableName(), (ApiKey{}).TableName()} {
		if !conn.Migrator().HasTable(table) {
			return nil, fmt.Errorf("认证表 %s 缺失或不可访问，请先完成正常升级和数据库初始化", table)
		}
	}
	var result AdminRecoveryResult
	err := conn.Transaction(func(tx *gorm.DB) error {
		var users []User
		if err := tx.Limit(2).Find(&users).Error; err != nil {
			return fmt.Errorf("读取管理员失败：%w", err)
		}
		if len(users) == 0 {
			return fmt.Errorf("管理员不存在，请正常启动服务并完成首次管理员初始化")
		}
		if len(users) != 1 {
			return fmt.Errorf("检测到多个管理员，拒绝自动恢复")
		}
		user := users[0]
		result = AdminRecoveryResult{UserID: user.ID, Username: user.Username, Deleted: deleteAdmin}
		if deleteAdmin {
			// 单管理员重建必须同时撤销历史孤立密钥，Webhook 不一定再次校验用户。
			if err := tx.Where("1 = 1").Delete(&ApiKey{}).Error; err != nil {
				return fmt.Errorf("清理 API Key 失败：%w", err)
			}
			if err := tx.Where("1 = 1").Delete(&UserSession{}).Error; err != nil {
				return fmt.Errorf("清理浏览器会话失败：%w", err)
			}
			deleted := tx.Delete(&User{}, user.ID)
			if deleted.Error != nil {
				return fmt.Errorf("删除管理员失败：%w", deleted.Error)
			}
			if deleted.RowsAffected != 1 {
				return gorm.ErrRecordNotFound
			}
			return nil
		}

		for range 32 {
			password, err := GenerateSessionSecret(18)
			if err != nil {
				return err
			}
			if ValidateUserPassword(password) == nil {
				result.Password = password
				break
			}
		}
		if result.Password == "" {
			return fmt.Errorf("无法按当前密码规则生成随机密码")
		}
		hash, err := HashUserPassword(result.Password)
		if err != nil {
			return fmt.Errorf("生成新密码哈希失败：%w", err)
		}
		updated := tx.Model(&User{}).Where("id = ?", user.ID).Updates(map[string]any{
			"password": hash, "two_factor_enabled": false,
			"two_factor_secret": "", "two_factor_pending_secret": "",
		})
		if updated.Error != nil {
			return fmt.Errorf("重置管理员密码失败：%w", updated.Error)
		}
		if updated.RowsAffected != 1 {
			return gorm.ErrRecordNotFound
		}
		return revokeAllUserSessions(tx, user.ID, "admin_recovery")
	})
	if err != nil {
		return nil, err
	}
	return &result, nil
}
