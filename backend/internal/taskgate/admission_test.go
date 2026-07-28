package taskgate

import (
	"errors"
	"runtime"
	"testing"
	"time"
)

func TestTaskAdmissionGateBlocksAndAllowsNewTasks(t *testing.T) {
	AllowNewTasks()
	t.Cleanup(AllowNewTasks)

	if IsBlocked() {
		t.Fatal("新建准入屏障不应阻止任务")
	}

	BlockNewTasks()
	if !IsBlocked() {
		t.Fatal("BlockNewTasks() 后必须阻止任务")
	}

	AllowNewTasks()
	if IsBlocked() {
		t.Fatal("AllowNewTasks() 后必须恢复任务准入")
	}
}

func TestBlockNewTasksWaitsForAdmittedTaskToRegister(t *testing.T) {
	AllowNewTasks()
	t.Cleanup(AllowNewTasks)

	release, err := Admit()
	if err != nil {
		t.Fatalf("Admit() error = %v", err)
	}
	defer release()

	blocked := make(chan struct{})
	go func() {
		BlockNewTasks()
		close(blocked)
	}()

	deadline := time.Now().Add(time.Second)
	for !IsBlocked() {
		if time.Now().After(deadline) {
			t.Fatal("BlockNewTasks() 未关闭任务准入")
		}
		runtime.Gosched()
	}
	select {
	case <-blocked:
		t.Fatal("BlockNewTasks() 不得在已准入任务登记前返回")
	default:
	}
	if _, err := Admit(); !errors.Is(err, ErrTaskAdmissionBlocked) {
		t.Fatalf("被阻塞时 Admit() error = %v，期望 ErrTaskAdmissionBlocked", err)
	}

	release()
	select {
	case <-blocked:
	case <-time.After(time.Second):
		t.Fatal("已准入任务释放后 BlockNewTasks() 未返回")
	}
}
