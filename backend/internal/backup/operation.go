package backup

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"time"
)

// operationStateFileName 是受限状态目录中记录最近一次操作的文件名。
// 状态文件位于业务数据库和可恢复白名单之外，使恢复覆盖配置和数据后仍可读出终态。
const operationStateFileName = "operation.json"

// operationStateMaxSize 限制状态文件体积，避免损坏或被替换的文件占用内存。
const operationStateMaxSize = 1 << 20

// OperationKind 区分协调器接管的实际操作类型。
type OperationKind string

const (
	OperationKindBackup  OperationKind = "backup"
	OperationKindRestore OperationKind = "restore"
)

// OperationState 是备份与恢复共用的状态机取值。
type OperationState string

const (
	OperationStateQueued          OperationState = "queued"
	OperationStateWaitingForTasks OperationState = "waiting_for_tasks"
	OperationStateValidating      OperationState = "validating"
	OperationStateRunning         OperationState = "running"
	OperationStateRollingBack     OperationState = "rolling_back"
	OperationStateCompleted       OperationState = "completed"
	OperationStateFailed          OperationState = "failed"
	OperationStateCancelled       OperationState = "cancelled"
)

// RollbackState 只用于恢复，独立记录自动回滚结果，使「恢复失败」与「回滚失败」不被混为一谈。
type RollbackState string

const (
	RollbackStateNotStarted RollbackState = "not_started"
	RollbackStateSucceeded  RollbackState = "succeeded"
	RollbackStateFailed     RollbackState = "failed"
)

// OperationErrorCode 是稳定的用户可读错误码。
// 它同时用于前端文案映射和脱敏日志，禁止携带路径、数据库标识或底层错误文本。
type OperationErrorCode string

const (
	OperationErrorCodeBackupFailed        OperationErrorCode = "backup_failed"
	OperationErrorCodeRestoreFailed       OperationErrorCode = "restore_failed"
	OperationErrorCodeArtifactInvalid     OperationErrorCode = "artifact_invalid"
	OperationErrorCodePasswordOrCorrupt   OperationErrorCode = "password_or_corrupt"
	OperationErrorCodeIncompatibleTarget  OperationErrorCode = "incompatible_target"
	OperationErrorCodeDatabaseUnavailable OperationErrorCode = "database_unavailable"
	OperationErrorCodeInsufficientSpace   OperationErrorCode = "insufficient_space"
	OperationErrorCodeSnapshotFailed      OperationErrorCode = "snapshot_failed"
	OperationErrorCodeTasksNotIdle        OperationErrorCode = "tasks_not_idle"
	OperationErrorCodeInternal            OperationErrorCode = "internal"
)

// operationErrorDescriptions 是每个错误码预定义的安全说明。
// 日志和响应只能输出这里的固定文案，不得拼接底层 error。
var operationErrorDescriptions = map[OperationErrorCode]string{
	OperationErrorCodeBackupFailed:        "备份失败",
	OperationErrorCodeRestoreFailed:       "恢复失败",
	OperationErrorCodeArtifactInvalid:     "备份工件校验失败",
	OperationErrorCodePasswordOrCorrupt:   "密码错误或工件损坏",
	OperationErrorCodeIncompatibleTarget:  "备份与目标数据库不兼容",
	OperationErrorCodeDatabaseUnavailable: "数据库连接失败",
	OperationErrorCodeInsufficientSpace:   "磁盘空间不足",
	OperationErrorCodeSnapshotFailed:      "预恢复快照创建失败",
	OperationErrorCodeTasksNotIdle:        "运行中的任务未在等待时间内静止",
	OperationErrorCodeInternal:            "内部错误",
}

// SafeDescription 返回错误码预定义的安全说明；未知错误码统一降级为内部错误。
func (code OperationErrorCode) SafeDescription() string {
	if description, found := operationErrorDescriptions[code]; found {
		return description
	}
	return operationErrorDescriptions[OperationErrorCodeInternal]
}

// OperationPhase 是恢复过程中的不可逆边界，用于启动期判断需要回滚到哪一步。
type OperationPhase string

const (
	OperationPhaseValidated        OperationPhase = "validated"
	OperationPhaseSnapshotReady    OperationPhase = "snapshot_ready"
	OperationPhaseDatabaseSwitched OperationPhase = "database_switched"
	OperationPhaseConfigSwitched   OperationPhase = "config_switched"
	OperationPhaseLogsSwitched     OperationPhase = "logs_switched"
	OperationPhaseTerminal         OperationPhase = "terminal"
)

var operationPhases = []OperationPhase{
	OperationPhaseValidated,
	OperationPhaseSnapshotReady,
	OperationPhaseDatabaseSwitched,
	OperationPhaseConfigSwitched,
	OperationPhaseLogsSwitched,
	OperationPhaseTerminal,
}

// operationTransitions 定义唯一合法的状态迁移表；终态没有出边。
var operationTransitions = map[OperationState][]OperationState{
	OperationStateQueued: {
		OperationStateWaitingForTasks,
		OperationStateValidating,
		OperationStateCancelled,
		OperationStateFailed,
	},
	OperationStateWaitingForTasks: {
		OperationStateValidating,
		OperationStateCancelled,
		OperationStateFailed,
	},
	OperationStateValidating: {
		OperationStateRunning,
		OperationStateCancelled,
		OperationStateFailed,
	},
	OperationStateRunning: {
		OperationStateRollingBack,
		OperationStateCompleted,
		OperationStateFailed,
	},
	OperationStateRollingBack: {
		OperationStateFailed,
	},
}

var (
	// ErrOperationInProgress 表示协调器已被另一个实际备份或恢复占用。
	ErrOperationInProgress = errors.New("已有备份或恢复正在进行")
	// ErrOperationNotFound 表示 operation ID 不是最近一次操作。
	ErrOperationNotFound = errors.New("备份操作不存在")
	// ErrOperationUnauthorized 表示一次性状态令牌缺失或不匹配。
	ErrOperationUnauthorized = errors.New("备份操作令牌无效")
	// ErrInvalidOperationTransition 表示请求的状态迁移不在状态机中。
	ErrInvalidOperationTransition = errors.New("非法的备份操作状态迁移")
	// ErrInvalidOperationState 表示持久化状态文件不可解析或不符合状态机。
	ErrInvalidOperationState = errors.New("备份操作状态文件无效")
)

// OperationProgress 是状态查询展示的非敏感进度，不含路径或数据库标识。
type OperationProgress struct {
	Message   string `json:"message,omitempty"`
	Completed int    `json:"completed"`
	Total     int    `json:"total"`
}

// OperationPhaseEntry 是一条已 fsync 的阶段日志。
type OperationPhaseEntry struct {
	Phase      OperationPhase `json:"phase"`
	RecordedAt int64          `json:"recorded_at"`
}

// OperationGrant 是协调器接管操作后一次性返回的凭据。
// Token 只在受理响应中明文出现一次，此后仅以 SHA-256 持久化。
type OperationGrant struct {
	OperationID string
	Token       string
}

// OperationView 是通过 operation ID 与令牌读取的操作状态。
type OperationView struct {
	OperationID   string             `json:"operation_id"`
	Kind          OperationKind      `json:"kind"`
	State         OperationState     `json:"state"`
	Progress      OperationProgress  `json:"progress"`
	ErrorCode     OperationErrorCode `json:"error_code,omitempty"`
	RollbackState RollbackState      `json:"rollback_state,omitempty"`
	StartedAt     int64              `json:"started_at"`
	UpdatedAt     int64              `json:"updated_at"`
	CompletedAt   int64              `json:"completed_at,omitempty"`
}

// TerminalOperation 是备份列表使用的脱敏终态，不含 operation ID、令牌、进度和原始错误。
type TerminalOperation struct {
	Kind          OperationKind      `json:"kind"`
	State         OperationState     `json:"state"`
	CompletedAt   int64              `json:"completed_at"`
	ErrorCode     OperationErrorCode `json:"error_code,omitempty"`
	RollbackState RollbackState      `json:"rollback_state,omitempty"`
}

// OperationTransition 描述一次状态迁移及其附带的终态信息。
type OperationTransition struct {
	State         OperationState
	ErrorCode     OperationErrorCode
	RollbackState RollbackState
	Progress      *OperationProgress
}

// operationRecord 是持久化到受限状态目录的最近一次操作。
type operationRecord struct {
	OperationID   string                `json:"operation_id"`
	Kind          OperationKind         `json:"kind"`
	State         OperationState        `json:"state"`
	TokenSHA256   string                `json:"token_sha256,omitempty"`
	Progress      OperationProgress     `json:"progress"`
	ErrorCode     OperationErrorCode    `json:"error_code,omitempty"`
	RollbackState RollbackState         `json:"rollback_state,omitempty"`
	Maintenance   bool                  `json:"maintenance"`
	Phases        []OperationPhaseEntry `json:"phases,omitempty"`
	StartedAt     int64                 `json:"started_at"`
	UpdatedAt     int64                 `json:"updated_at"`
	CompletedAt   int64                 `json:"completed_at,omitempty"`
}

// OperationCoordinator 在单实例内互斥地接管实际备份或恢复。
// 它只保留最近一次操作：下一次操作取得执行权时原子替换，不提供历史审计。
type OperationCoordinator struct {
	stateDir       string
	statePath      string
	mutex          sync.Mutex
	record         *operationRecord
	maintenance    atomic.Bool
	inventoryReads *inventoryReadRegistry
}

// NewOperationCoordinator 打开受限状态目录，并载入上一次进程留下的最近一次操作。
// 载入非终态操作不会自行收敛，由启动期检查决定回滚或标记失败。
func NewOperationCoordinator(stateDir string) (*OperationCoordinator, error) {
	if stateDir == "" {
		return nil, fmt.Errorf("%w：状态目录不能为空", ErrInvalidOperationState)
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return nil, fmt.Errorf("创建备份状态目录: %w", err)
	}
	coordinator := &OperationCoordinator{
		stateDir:       stateDir,
		statePath:      filepath.Join(stateDir, operationStateFileName),
		inventoryReads: globalInventoryReads,
	}
	record, err := readOperationRecord(coordinator.statePath)
	if err != nil {
		return nil, err
	}
	coordinator.record = record
	if record != nil {
		coordinator.maintenance.Store(record.Maintenance)
	}
	if record != nil && !isTerminalOperationState(record.State) {
		coordinator.inventoryReads.invalidate()
	} else {
		coordinator.inventoryReads.resume()
	}
	return coordinator, nil
}

func readOperationRecord(statePath string) (*operationRecord, error) {
	file, err := os.Open(statePath)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取备份状态文件: %w", err)
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, operationStateMaxSize+1))
	if err != nil {
		return nil, fmt.Errorf("读取备份状态文件: %w", err)
	}
	if len(data) > operationStateMaxSize {
		return nil, fmt.Errorf("%w：状态文件超出限制", ErrInvalidOperationState)
	}
	var record operationRecord
	if err := decodeStrictJSON(data, &record); err != nil {
		return nil, fmt.Errorf("%w：状态文件格式无效", ErrInvalidOperationState)
	}
	if err := record.validate(); err != nil {
		return nil, err
	}
	return &record, nil
}

func (record operationRecord) validate() error {
	if !isOperationID(record.OperationID) {
		return fmt.Errorf("%w：操作标识无效", ErrInvalidOperationState)
	}
	if record.Kind != OperationKindBackup && record.Kind != OperationKindRestore {
		return fmt.Errorf("%w：操作类型无效", ErrInvalidOperationState)
	}
	if !isKnownOperationState(record.State) {
		return fmt.Errorf("%w：操作状态无效", ErrInvalidOperationState)
	}
	if record.TokenSHA256 != "" && !isSHA256(record.TokenSHA256) {
		return fmt.Errorf("%w：操作令牌散列无效", ErrInvalidOperationState)
	}
	if record.RollbackState != "" {
		if record.Kind != OperationKindRestore || !isKnownRollbackState(record.RollbackState) {
			return fmt.Errorf("%w：回滚状态无效", ErrInvalidOperationState)
		}
	}
	for _, entry := range record.Phases {
		if !slices.Contains(operationPhases, entry.Phase) {
			return fmt.Errorf("%w：阶段日志无效", ErrInvalidOperationState)
		}
	}
	if record.StartedAt <= 0 {
		return fmt.Errorf("%w：操作时间无效", ErrInvalidOperationState)
	}
	return nil
}

// Begin 取得单实例执行权并原子替换上一次的终态。
// issueToken 为 false 时不生成一次性令牌，用于没有浏览器调用方的定时备份。
func (coordinator *OperationCoordinator) Begin(kind OperationKind, issueToken bool) (OperationGrant, error) {
	if kind != OperationKindBackup && kind != OperationKindRestore {
		return OperationGrant{}, fmt.Errorf("%w：操作类型无效", ErrInvalidOperationState)
	}

	coordinator.mutex.Lock()
	defer coordinator.mutex.Unlock()
	if coordinator.record != nil && !isTerminalOperationState(coordinator.record.State) {
		return OperationGrant{}, ErrOperationInProgress
	}

	operationID, err := newOperationID()
	if err != nil {
		return OperationGrant{}, err
	}
	grant := OperationGrant{OperationID: operationID}
	tokenHash := ""
	if issueToken {
		token, err := newOperationToken()
		if err != nil {
			return OperationGrant{}, err
		}
		grant.Token = token
		tokenHash = sha256Hex([]byte(token))
	}

	now := time.Now().Unix()
	previous := coordinator.record
	coordinator.record = &operationRecord{
		OperationID: operationID,
		Kind:        kind,
		State:       OperationStateQueued,
		TokenSHA256: tokenHash,
		StartedAt:   now,
		UpdatedAt:   now,
	}
	if kind == OperationKindRestore {
		coordinator.record.RollbackState = RollbackStateNotStarted
	}
	if err := coordinator.persistLocked(); err != nil {
		coordinator.record = previous
		return OperationGrant{}, err
	}
	coordinator.maintenance.Store(false)
	// 实际操作一旦成功取得执行权，目录清点立即失效；这里绝不等待其磁盘 I/O。
	coordinator.inventoryReads.invalidate()
	return grant, nil
}

// Authorize 以 operation ID 和一次性令牌读取最近一次操作；没有令牌的操作不可查询。
func (coordinator *OperationCoordinator) Authorize(operationID string, token string) (OperationView, error) {
	coordinator.mutex.Lock()
	defer coordinator.mutex.Unlock()
	record := coordinator.record
	if record == nil || record.OperationID != operationID {
		return OperationView{}, ErrOperationNotFound
	}
	if record.TokenSHA256 == "" || token == "" {
		return OperationView{}, ErrOperationUnauthorized
	}
	if subtle.ConstantTimeCompare([]byte(sha256Hex([]byte(token))), []byte(record.TokenSHA256)) != 1 {
		return OperationView{}, ErrOperationUnauthorized
	}
	return record.view(), nil
}

// Active 返回仍未终态的操作视图，供任务闸门与启动期收敛判断当前是否有未完成操作。
func (coordinator *OperationCoordinator) Active() *OperationView {
	coordinator.mutex.Lock()
	defer coordinator.mutex.Unlock()
	if coordinator.record == nil || isTerminalOperationState(coordinator.record.State) {
		return nil
	}
	view := coordinator.record.view()
	return &view
}

// LatestTerminal 返回最近一次终态的脱敏结果，供备份列表在服务恢复后收敛展示。
func (coordinator *OperationCoordinator) LatestTerminal() *TerminalOperation {
	coordinator.mutex.Lock()
	defer coordinator.mutex.Unlock()
	record := coordinator.record
	if record == nil || !isTerminalOperationState(record.State) {
		return nil
	}
	return &TerminalOperation{
		Kind:          record.Kind,
		State:         record.State,
		CompletedAt:   record.CompletedAt,
		ErrorCode:     record.ErrorCode,
		RollbackState: record.RollbackState,
	}
}

// Phases 返回已持久化的阶段日志，供启动期判断恢复中断在哪一个不可逆边界之后。
func (coordinator *OperationCoordinator) Phases(operationID string) ([]OperationPhaseEntry, error) {
	coordinator.mutex.Lock()
	defer coordinator.mutex.Unlock()
	if coordinator.record == nil || coordinator.record.OperationID != operationID {
		return nil, ErrOperationNotFound
	}
	return append([]OperationPhaseEntry(nil), coordinator.record.Phases...), nil
}

// Transition 按状态机推进操作；进入终态时同时解除维护屏障并记录完成时间。
func (coordinator *OperationCoordinator) Transition(operationID string, transition OperationTransition) error {
	coordinator.mutex.Lock()
	defer coordinator.mutex.Unlock()
	record := coordinator.record
	if record == nil || record.OperationID != operationID {
		return ErrOperationNotFound
	}
	if !slices.Contains(operationTransitions[record.State], transition.State) {
		return ErrInvalidOperationTransition
	}
	if transition.RollbackState != "" {
		if record.Kind != OperationKindRestore || !isKnownRollbackState(transition.RollbackState) {
			return ErrInvalidOperationTransition
		}
	}

	previous := *record
	record.State = transition.State
	record.UpdatedAt = time.Now().Unix()
	if transition.Progress != nil {
		record.Progress = *transition.Progress
	}
	if transition.ErrorCode != "" {
		record.ErrorCode = transition.ErrorCode
	}
	if transition.RollbackState != "" {
		record.RollbackState = transition.RollbackState
	}
	if isTerminalOperationState(transition.State) {
		record.CompletedAt = record.UpdatedAt
		record.Maintenance = false
	}
	if err := coordinator.persistLocked(); err != nil {
		*record = previous
		return err
	}
	if isTerminalOperationState(transition.State) {
		coordinator.maintenance.Store(false)
		coordinator.inventoryReads.resume()
	}
	return nil
}

// UpdateProgress 只更新非敏感进度，不改变状态机。
func (coordinator *OperationCoordinator) UpdateProgress(operationID string, progress OperationProgress) error {
	coordinator.mutex.Lock()
	defer coordinator.mutex.Unlock()
	record := coordinator.record
	if record == nil || record.OperationID != operationID {
		return ErrOperationNotFound
	}
	previous := *record
	record.Progress = progress
	record.UpdatedAt = time.Now().Unix()
	if err := coordinator.persistLocked(); err != nil {
		*record = previous
		return err
	}
	return nil
}

// RecordPhase 在不可逆边界之前后原子写入并 fsync 阶段日志；重复记录同一阶段是幂等的。
func (coordinator *OperationCoordinator) RecordPhase(operationID string, phase OperationPhase) error {
	if !slices.Contains(operationPhases, phase) {
		return fmt.Errorf("%w：阶段无效", ErrInvalidOperationState)
	}
	coordinator.mutex.Lock()
	defer coordinator.mutex.Unlock()
	record := coordinator.record
	if record == nil || record.OperationID != operationID {
		return ErrOperationNotFound
	}
	for _, entry := range record.Phases {
		if entry.Phase == phase {
			return nil
		}
	}
	previous := *record
	record.Phases = append(append([]OperationPhaseEntry(nil), record.Phases...), OperationPhaseEntry{
		Phase:      phase,
		RecordedAt: time.Now().Unix(),
	})
	record.UpdatedAt = time.Now().Unix()
	if err := coordinator.persistLocked(); err != nil {
		*record = previous
		return err
	}
	return nil
}

// SetMaintenance 切换维护写入屏障；只有仍在进行的操作可以启用维护。
func (coordinator *OperationCoordinator) SetMaintenance(operationID string, enabled bool) error {
	coordinator.mutex.Lock()
	defer coordinator.mutex.Unlock()
	record := coordinator.record
	if record == nil || record.OperationID != operationID {
		return ErrOperationNotFound
	}
	if enabled && isTerminalOperationState(record.State) {
		return ErrInvalidOperationTransition
	}
	if record.Maintenance == enabled {
		return nil
	}
	previous := *record
	record.Maintenance = enabled
	record.UpdatedAt = time.Now().Unix()
	if err := coordinator.persistLocked(); err != nil {
		*record = previous
		return err
	}
	coordinator.maintenance.Store(enabled)
	return nil
}

// InMaintenance 供认证前的维护中间件无锁读取当前是否处于维护屏障中。
func (coordinator *OperationCoordinator) InMaintenance() bool {
	return coordinator.maintenance.Load()
}

func (coordinator *OperationCoordinator) persistLocked() error {
	data, err := json.Marshal(coordinator.record)
	if err != nil {
		return fmt.Errorf("编码备份操作状态: %w", err)
	}
	return replaceFileAtomically(coordinator.statePath, func(output *os.File) error {
		if _, err := output.Write(data); err != nil {
			return fmt.Errorf("写入备份操作状态: %w", err)
		}
		return nil
	})
}

func (record operationRecord) view() OperationView {
	return OperationView{
		OperationID:   record.OperationID,
		Kind:          record.Kind,
		State:         record.State,
		Progress:      record.Progress,
		ErrorCode:     record.ErrorCode,
		RollbackState: record.RollbackState,
		StartedAt:     record.StartedAt,
		UpdatedAt:     record.UpdatedAt,
		CompletedAt:   record.CompletedAt,
	}
}

func isTerminalOperationState(state OperationState) bool {
	switch state {
	case OperationStateCompleted, OperationStateFailed, OperationStateCancelled:
		return true
	}
	return false
}

func isKnownOperationState(state OperationState) bool {
	if isTerminalOperationState(state) {
		return true
	}
	_, found := operationTransitions[state]
	return found
}

func isKnownOperationPhase(phase OperationPhase) bool {
	return slices.Contains(operationPhases, phase)
}

func normalizedOperationErrorCode(code OperationErrorCode) OperationErrorCode {
	if _, found := operationErrorDescriptions[code]; found {
		return code
	}
	return OperationErrorCodeInternal
}

func isKnownRollbackState(state RollbackState) bool {
	switch state {
	case RollbackStateNotStarted, RollbackStateSucceeded, RollbackStateFailed:
		return true
	}
	return false
}

func isOperationID(value string) bool {
	return isArtifactID(value)
}

func newOperationID() (string, error) {
	identifier, err := newArtifactID()
	if err != nil {
		return "", fmt.Errorf("生成备份操作标识: %w", err)
	}
	return identifier, nil
}

func newOperationToken() (string, error) {
	token := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, token); err != nil {
		return "", fmt.Errorf("生成备份操作令牌: %w", err)
	}
	return hex.EncodeToString(token), nil
}
