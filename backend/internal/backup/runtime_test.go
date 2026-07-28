package backup

import (
	"bytes"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"qmediasync/internal/helpers"
)

// TestRuntimeDirectoriesStayOutsideRestorableScope 保护目录归属：
// 运行状态目录必须在工件目录和上传暂存目录之外，否则恢复覆盖数据后读不出准确终态。
func TestRuntimeDirectoriesStayOutsideRestorableScope(t *testing.T) {
	helpers.ConfigDir = t.TempDir()

	stateDir := StateDir()
	artifactDir := ArtifactDir()
	stagingDir := UploadStagingDir()

	for _, test := range []struct {
		name  string
		child string
		root  string
	}{
		{name: "状态目录不在工件目录内", child: stateDir, root: artifactDir},
		{name: "状态目录不在上传暂存目录内", child: stateDir, root: stagingDir},
		{name: "工件目录不在上传暂存目录内", child: artifactDir, root: stagingDir},
		{name: "上传暂存目录不在工件目录内", child: stagingDir, root: artifactDir},
	} {
		t.Run(test.name, func(t *testing.T) {
			relative, err := filepath.Rel(test.root, test.child)
			if err != nil {
				t.Fatalf("filepath.Rel(%q, %q): %v", test.root, test.child, err)
			}
			if !strings.HasPrefix(relative, "..") {
				t.Fatalf("%q 位于 %q 之内", test.child, test.root)
			}
		})
	}

	// 上传暂存必须是 config/tmp 的专属子目录，清理时才不会波及其他功能。
	if filepath.Base(stagingDir) != "backup-restore" ||
		filepath.Base(filepath.Dir(stagingDir)) != "tmp" {
		t.Fatalf("UploadStagingDir() = %q, want config/tmp/backup-restore", stagingDir)
	}
}

// TestOperationErrorCodeForClassifiesWrappedCauses 保护错误归类：
// 状态与日志只暴露稳定错误码，因此归类必须穿透 %w 链，用 %v 拼接的错误会退化成兜底码。
func TestOperationErrorCodeForClassifiesWrappedCauses(t *testing.T) {
	for _, test := range []struct {
		name     string
		err      error
		fallback OperationErrorCode
		want     OperationErrorCode
	}{
		{
			name:     "nil 不产生错误码",
			err:      nil,
			fallback: OperationErrorCodeBackupFailed,
			want:     "",
		},
		{
			name:     "密码错误或载荷损坏",
			err:      fmt.Errorf("解密载荷：%w", ErrArtifactPasswordOrCorrupt),
			fallback: OperationErrorCodeRestoreFailed,
			want:     OperationErrorCodePasswordOrCorrupt,
		},
		{
			name:     "工件结构非法",
			err:      fmt.Errorf("读取内层归档：%w", ErrInvalidArtifact),
			fallback: OperationErrorCodeRestoreFailed,
			want:     OperationErrorCodeArtifactInvalid,
		},
		{
			name:     "多层包装后的文件缺失",
			err:      fmt.Errorf("恢复失败：%w", fmt.Errorf("打开备份文件失败：%w", os.ErrNotExist)),
			fallback: OperationErrorCodeRestoreFailed,
			want:     OperationErrorCodeArtifactInvalid,
		},
		{
			name:     "磁盘空间不足",
			err:      fmt.Errorf("写入工件：%w", syscall.ENOSPC),
			fallback: OperationErrorCodeBackupFailed,
			want:     OperationErrorCodeInsufficientSpace,
		},
		{
			name:     "未识别的错误退回调用方给定的兜底码",
			err:      errors.New("未知故障"),
			fallback: OperationErrorCodeBackupFailed,
			want:     OperationErrorCodeBackupFailed,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := operationErrorCodeFor(test.err, test.fallback); got != test.want {
				t.Fatalf("operationErrorCodeFor() = %q, want %q", got, test.want)
			}
		})
	}
}

// TestOperationErrorCodeForIgnoresUnwrappableCauses 固化 %w 的必要性：
// 一旦有人把 %w 改回 %v，归类会立刻退化，这条用例就会失败。
func TestOperationErrorCodeForIgnoresUnwrappableCauses(t *testing.T) {
	unwrappable := fmt.Errorf("打开备份文件失败：%v", os.ErrNotExist)
	if got := operationErrorCodeFor(unwrappable, OperationErrorCodeRestoreFailed); got != OperationErrorCodeRestoreFailed {
		t.Fatalf("operationErrorCodeFor() = %q, want %q", got, OperationErrorCodeRestoreFailed)
	}
}

// TestLogOperationDiagnosticOnlyEmitsNormalizedSafeFields 覆盖自动回滚失败诊断的日志边界：
// 诊断只允许 QLogger 输出规范 operation ID、阶段、稳定错误码和该错误码的固定说明。
func TestLogOperationDiagnosticOnlyEmitsNormalizedSafeFields(t *testing.T) {
	originalLogger := helpers.AppLogger
	defer func() { helpers.AppLogger = originalLogger }()

	var output bytes.Buffer
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(&output, "", 0)}
	LogOperationDiagnostic(
		testArtifactID,
		OperationPhaseSnapshotReady,
		OperationErrorCodeDatabaseUnavailable,
	)
	logged := output.String()
	for _, expected := range []string{
		"operation_id=" + testArtifactID,
		"phase=snapshot_ready",
		"error_code=database_unavailable",
		"description=数据库连接失败",
	} {
		if !strings.Contains(logged, expected) {
			t.Fatalf("诊断日志 = %q, want %q", logged, expected)
		}
	}

	output.Reset()
	const rawSecret = "password=rollback-secret token=rollback-token /private/database/path"
	LogOperationDiagnostic(rawSecret, OperationPhase(rawSecret), OperationErrorCode(rawSecret))
	logged = output.String()
	for _, expected := range []string{
		"operation_id=invalid",
		"phase=unknown",
		"error_code=internal",
		"description=内部错误",
	} {
		if !strings.Contains(logged, expected) {
			t.Fatalf("规范化诊断日志 = %q, want %q", logged, expected)
		}
	}
	if strings.Contains(logged, rawSecret) || strings.Contains(logged, "rollback-secret") ||
		strings.Contains(logged, "rollback-token") || strings.Contains(logged, "/private/database/path") {
		t.Fatalf("诊断日志泄露了未规范化输入：%q", logged)
	}
}
