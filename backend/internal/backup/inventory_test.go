package backup

import (
	"errors"
	"testing"
	"time"
)

// TestInventoryReadIsInvalidatedWithoutBlockingOperation 保护目录清点的低优先级语义：
// 实际备份和恢复接管时不会等待正在读取的清点；已失效轮次不能写索引，终态后才可重新登记。
func TestInventoryReadIsInvalidatedWithoutBlockingOperation(t *testing.T) {
	for _, test := range []struct {
		name string
		kind OperationKind
	}{
		{name: "备份接管", kind: OperationKindBackup},
		{name: "恢复接管", kind: OperationKindRestore},
	} {
		t.Run(test.name, func(t *testing.T) {
			registry := newInventoryReadRegistry()
			read, registered := registry.register()
			if !registered || !read.Valid() {
				t.Fatal("操作开始前目录清点必须可以登记")
			}

			readStarted := make(chan struct{})
			allowReadBoundary := make(chan struct{})
			scanFinished := make(chan error, 1)
			indexWrites := 0
			go func() {
				close(readStarted)
				<-allowReadBoundary // 模拟无法立即取消的目录读取或工件验证。
				scanFinished <- read.WriteIndex(func() error {
					indexWrites++
					return nil
				})
			}()
			<-readStarted

			coordinator, err := NewOperationCoordinator(t.TempDir())
			if err != nil {
				t.Fatalf("NewOperationCoordinator() error = %v", err)
			}
			coordinator.inventoryReads = registry

			beginFinished := make(chan error, 1)
			go func() {
				_, err := coordinator.Begin(test.kind, false)
				beginFinished <- err
			}()
			select {
			case err := <-beginFinished:
				if err != nil {
					t.Fatalf("Begin() error = %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("实际操作等待了目录清点的读取")
			}
			if read.Valid() {
				t.Fatal("实际操作接管后正在运行的清点必须立即失效")
			}

			close(allowReadBoundary)
			if err := <-scanFinished; !errors.Is(err, ErrInventoryReadInvalidated) {
				t.Fatalf("失效清点 WriteIndex() error = %v, want ErrInventoryReadInvalidated", err)
			}
			if indexWrites != 0 {
				t.Fatalf("失效清点写入索引次数 = %d, want 0", indexWrites)
			}

			active := coordinator.Active()
			if active == nil {
				t.Fatal("Begin() 后必须存在进行中的操作")
			}
			if err := coordinator.Transition(active.OperationID, OperationTransition{State: OperationStateCancelled}); err != nil {
				t.Fatalf("Transition(cancelled) error = %v", err)
			}
			nextRead, registered := registry.register()
			if !registered || !nextRead.Valid() {
				t.Fatal("操作终态后目录清点必须可以重新登记")
			}
			if err := nextRead.WriteIndex(func() error {
				indexWrites++
				return nil
			}); err != nil {
				t.Fatalf("终态后的清点 WriteIndex() error = %v", err)
			}
			if indexWrites != 1 {
				t.Fatalf("终态后的清点写入索引次数 = %d, want 1", indexWrites)
			}
		})
	}
}
