import { describe, expect, it } from 'vitest'

import { MIN_BACKUP_PASSWORD_LENGTH, validateBackupPassword } from '@/utils/backupPassword'

// 前端规则必须与后端 validation.BackupPassword 等价：服务端仍是最终裁决方，
// 这里保护的是「即时反馈不会误导用户」这一条边界。
describe('validateBackupPassword', () => {
  it('允许空密码表示不加密', () => {
    expect(validateBackupPassword('')).toBe('')
  })

  it('接受同时含大小写字母和数字且足够长的密码', () => {
    expect(validateBackupPassword('BackupPass123')).toBe('')
    expect(validateBackupPassword('备份密码Abc123456')).toBe('')
  })

  it('按 Unicode 码点计算长度', () => {
    expect(validateBackupPassword('Ab1' + '好'.repeat(MIN_BACKUP_PASSWORD_LENGTH - 4))).toContain(
      '长度',
    )
    expect(validateBackupPassword('Ab1' + '好'.repeat(MIN_BACKUP_PASSWORD_LENGTH - 3))).toBe('')
  })

  it('拒绝任何空白字符，包括全角空格与制表符', () => {
    for (const whitespace of [' ', '　', '\t', '\n']) {
      expect(validateBackupPassword(`Backup${whitespace}Pass123`)).toBe('不能包含空白字符')
    }
  })

  it('要求同时包含大写字母、小写字母和数字', () => {
    for (const password of ['backuppass123', 'BACKUPPASS123', 'BackupPassword']) {
      expect(validateBackupPassword(password)).toBe('必须包含大写字母、小写字母和数字')
    }
  })

  it('不裁剪首尾字符，也不做 Unicode 归一化', () => {
    expect(validateBackupPassword(' BackupPass123 ')).toBe('不能包含空白字符')
  })
})
