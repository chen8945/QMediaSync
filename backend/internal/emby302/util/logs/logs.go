package logs

import "qmediasync/internal/helpers"

// Info 输出 Info 日志。
func Info(format string, v ...any) {
	helpers.AppLogger.Infof(format, v...)
}

// Success 输出成功信息日志。
func Success(format string, v ...any) {
	helpers.AppLogger.Infof(format, v...)
}

// Warn 输出 Warn 日志。
func Warn(format string, v ...any) {
	helpers.AppLogger.Warnf(format, v...)
}

// Debug 输出 Debug 日志。
func Debug(format string, v ...any) {
	helpers.AppLogger.Debugf(format, v...)
}

// SensitiveDebug 输出可能包含敏感信息的 Debug 日志。
func SensitiveDebug(format string, v ...any) {
	helpers.AppLogger.SensitiveDebugf(format, v...)
}

// Error 输出 Error 日志。
func Error(format string, v ...any) {
	helpers.AppLogger.Errorf(format, v...)
}

// Tip 输出 Info 日志。
func Tip(format string, v ...any) {
	helpers.AppLogger.Infof(format, v...)
}

// Progress 输出 Info 日志。
func Progress(format string, v ...any) {
	helpers.AppLogger.Infof(format, v...)
}
