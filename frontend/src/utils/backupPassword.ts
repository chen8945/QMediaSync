/**
 * 备份工件密码的前端即时反馈规则，与后端 `validation.BackupPassword` 等价：
 * 空值表示不使用密码；非空时至少 10 个 Unicode 码点，且同时包含大写英文字母、
 * 小写英文字母和数字，并且不能包含任何 Unicode 空白字符（含全角空格）。
 *
 * 输入按不透明字符串处理：既不裁剪首尾字符，也不做 Unicode 归一化。
 * 前端只做即时反馈，服务端始终是最终裁决方。
 */
export const MIN_BACKUP_PASSWORD_LENGTH = 10

const WHITESPACE_PATTERN = /[\s]/u

export function validateBackupPassword(password: string): string {
  if (password === '') return ''
  if ([...password].length < MIN_BACKUP_PASSWORD_LENGTH) {
    return `长度不能小于 ${MIN_BACKUP_PASSWORD_LENGTH} 个字符`
  }
  if (WHITESPACE_PATTERN.test(password)) {
    return '不能包含空白字符'
  }
  if (!/[A-Z]/.test(password) || !/[a-z]/.test(password) || !/[0-9]/.test(password)) {
    return '必须包含大写字母、小写字母和数字'
  }
  return ''
}
