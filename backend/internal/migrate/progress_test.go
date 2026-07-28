package migrate

import (
	"encoding/json"
	"testing"
)

// TestProgressLifecycleKeepsRunningFlagAccurate 覆盖迁移进度的生命周期：
// 迁移自带进度状态，不复用常规备份的操作协调器，运行标记必须只由开始与结束改写。
func TestProgressLifecycleKeepsRunningFlagAccurate(t *testing.T) {
	t.Cleanup(func() {
		progressMutex.Lock()
		currentProgress = Progress{}
		progressMutex.Unlock()
	})

	if CurrentProgress().IsRunning {
		t.Fatal("初始状态不应是运行中")
	}

	if !startProgress("backup", "开始导出", 3) {
		t.Fatal("startProgress() 未开始迁移")
	}
	if !CurrentProgress().IsRunning {
		t.Fatal("startProgress 后应处于运行中")
	}
	snapshot := CurrentProgress()
	if snapshot.Type != "backup" || snapshot.Total != 3 || snapshot.Count != 0 {
		t.Fatalf("CurrentProgress() = %+v, want type=backup total=3 count=0", snapshot)
	}

	updateProgress("已导出 2 张表", 2, "")
	snapshot = CurrentProgress()
	if snapshot.Count != 2 || snapshot.Desc != "已导出 2 张表" {
		t.Fatalf("CurrentProgress() = %+v, want count=2", snapshot)
	}
	if !snapshot.IsRunning {
		t.Fatal("更新进度不得结束运行状态")
	}

	finishProgress("导出完成", "")
	snapshot = CurrentProgress()
	if snapshot.IsRunning {
		t.Fatal("finishProgress 后不应仍是运行中")
	}
	if snapshot.Count != 2 {
		t.Fatalf("结束后 Count = %d, want 2", snapshot.Count)
	}

	if !startProgress("import", "开始导入", 5) {
		t.Fatal("startProgress() 未开始下一次迁移")
	}
	snapshot = CurrentProgress()
	if snapshot.Count != 0 || snapshot.ErrorMsg != "" || snapshot.Total != 5 {
		t.Fatalf("新一次操作未重置进度：%+v", snapshot)
	}
}

func TestStartProgressRejectsConcurrentOperation(t *testing.T) {
	t.Cleanup(func() {
		progressMutex.Lock()
		currentProgress = Progress{}
		progressMutex.Unlock()
	})

	if !startProgress("backup", "开始导出", 3) {
		t.Fatal("初始迁移应能开始")
	}
	if startProgress("backup", "重复导出", 3) {
		t.Fatal("运行中的迁移不应被重复启动")
	}
	snapshot := CurrentProgress()
	if snapshot.Desc != "开始导出" || snapshot.Type != "backup" {
		t.Fatalf("重复启动不应覆盖现有进度：%+v", snapshot)
	}
}

// TestProgressJSONFieldNamesStayStable 固化前端依赖的字段名：
// 迁移页面直接读取这些键，改名属于破坏性变更。
func TestProgressJSONFieldNamesStayStable(t *testing.T) {
	payload, err := json.Marshal(Progress{})
	if err != nil {
		t.Fatalf("json.Marshal(Progress) error = %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("json.Unmarshal error = %v", err)
	}

	for _, field := range []string{"type", "desc", "total", "count", "error_msg", "is_running", "start_time", "elapsed"} {
		if _, found := decoded[field]; !found {
			t.Fatalf("迁移进度缺少字段 %q：%s", field, payload)
		}
	}
	if len(decoded) != 8 {
		t.Fatalf("迁移进度字段数 = %d, want 8：%s", len(decoded), payload)
	}
}
