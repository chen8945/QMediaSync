package backup

import (
	"errors"
	"sync/atomic"
)

// ErrInventoryReadInvalidated 表示低优先级目录清点已被实际备份或恢复抢占。
// 调用方必须在每个扫描、验证和写入索引的安全边界处理该错误，且不能再写入 BackupRecord。
var ErrInventoryReadInvalidated = errors.New("备份目录清点已失效")

// InventoryRead 是一次低优先级目录清点的可失效注册。
// 它不取消正在进行的磁盘 I/O；清点器在下一个安全边界检查 Valid 后自行退出，
// 从而实际备份或恢复不需要等待目录读取或工件验证完成。
type InventoryRead struct {
	registry   *inventoryReadRegistry
	generation uint64
}

// Valid 判断当前清点轮次是否仍可继续读取、验证或写入索引。
func (read InventoryRead) Valid() bool {
	return read.registry != nil && read.registry.accepting.Load() &&
		read.registry.generation.Load() == read.generation
}

// WriteIndex 在写入 BackupRecord 前再次检查当前轮次有效性。
// 目录清点器必须通过此方法提交索引更新，避免失效轮次覆盖实际操作之后的记录。
func (read InventoryRead) WriteIndex(write func() error) error {
	if !read.Valid() {
		return ErrInventoryReadInvalidated
	}
	if write == nil {
		return nil
	}
	return write()
}

// inventoryReadRegistry 为单进程目录清点保存当前代数和是否接受新扫描。
// 失效只改写原子状态，不等待正在进行的 I/O。
type inventoryReadRegistry struct {
	generation atomic.Uint64
	accepting  atomic.Bool
}

func newInventoryReadRegistry() *inventoryReadRegistry {
	registry := &inventoryReadRegistry{}
	registry.generation.Store(1)
	registry.accepting.Store(true)
	return registry
}

// register 在没有实际操作时登记一个新的低优先级读轮次。
func (registry *inventoryReadRegistry) register() (InventoryRead, bool) {
	if !registry.accepting.Load() {
		return InventoryRead{}, false
	}
	generation := registry.generation.Load()
	if !registry.accepting.Load() || registry.generation.Load() != generation {
		return InventoryRead{}, false
	}
	return InventoryRead{registry: registry, generation: generation}, true
}

// invalidate 让所有已登记轮次立即失效，并阻止操作终态前的新轮次。
func (registry *inventoryReadRegistry) invalidate() {
	registry.accepting.Store(false)
	registry.generation.Add(1)
}

// resume 只在实际操作进入终态后重新接受目录清点。
func (registry *inventoryReadRegistry) resume() {
	registry.accepting.Store(true)
}

var globalInventoryReads = newInventoryReadRegistry()

// BeginInventoryRead 为目录清点器登记一次低优先级读轮次。
// 实际备份或恢复尚未终态时返回 false；调用方应保留现有列表快照而不启动扫描。
func BeginInventoryRead() (InventoryRead, bool) {
	return globalInventoryReads.register()
}
