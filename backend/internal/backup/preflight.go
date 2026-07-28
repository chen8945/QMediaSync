package backup

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"qmediasync/internal/helpers"
)

// PreflightValidity 是预检成功后固定的有效期。
// 它不随工件大小或用户操作延长；过期后必须重新完成完整预检。
const PreflightValidity = 30 * time.Minute

// preflightStateFileName 是受限状态目录中保存预检记录的文件名。
const preflightStateFileName = "preflight.json"

// preflightStateMaxSize 限制预检记录文件体积。
const preflightStateMaxSize = 1 << 20

// ErrPreflightInvalid 统一表示预检记录不存在、已使用、已过期或与来源不一致。
// 它对外只映射为同一句用户文案，避免泄露判定细节。
var ErrPreflightInvalid = errors.New("预检记录无效或已过期")

// PreflightSourceKind 区分预检来源，确认阶段必须与预检阶段完全一致。
type PreflightSourceKind string

const (
	// PreflightSourceRecord 是备份列表中的已保存工件，含目录导入。
	PreflightSourceRecord PreflightSourceKind = "record"
	// PreflightSourceUpload 是上传到受限暂存目录的工件。
	PreflightSourceUpload PreflightSourceKind = "upload"
)

// PreflightSource 是确认阶段必须逐项复核的来源身份。
type PreflightSource struct {
	Kind           PreflightSourceKind
	RecordID       uint
	ArtifactPath   string
	ArtifactSHA256 string
}

// preflightRecord 是持久化的一次性预检记录。
// 它只保存不可逆的 ID 散列、来源身份与散列、非敏感目标标识、固定过期时间和使用状态，
// 不保存密码、派生密钥或 preflight_id 明文。
type preflightRecord struct {
	IDSHA256       string              `json:"id_sha256"`
	Kind           PreflightSourceKind `json:"kind"`
	RecordID       uint                `json:"record_id,omitempty"`
	ArtifactPath   string              `json:"artifact_path"`
	ArtifactSHA256 string              `json:"artifact_sha256"`
	TargetLabel    string              `json:"target_label"`
	IssuedAt       int64               `json:"issued_at"`
	ExpiresAt      int64               `json:"expires_at"`
	Used           bool                `json:"used"`
}

var preflightStoreMutex sync.Mutex

// IssuePreflight 在预检成功后登记一次性记录，并返回只在响应中出现一次的 preflight_id。
func IssuePreflight(source PreflightSource, targetLabel string) (string, int64, error) {
	if err := source.validate(); err != nil {
		return "", 0, err
	}
	identifier, err := newOperationToken()
	if err != nil {
		return "", 0, fmt.Errorf("生成预检标识: %w", err)
	}

	now := time.Now()
	record := preflightRecord{
		IDSHA256:       sha256Hex([]byte(identifier)),
		Kind:           source.Kind,
		RecordID:       source.RecordID,
		ArtifactPath:   source.ArtifactPath,
		ArtifactSHA256: source.ArtifactSHA256,
		TargetLabel:    targetLabel,
		IssuedAt:       now.Unix(),
		ExpiresAt:      now.Add(PreflightValidity).Unix(),
	}

	preflightStoreMutex.Lock()
	defer preflightStoreMutex.Unlock()
	records, err := loadPreflightRecords()
	if err != nil {
		return "", 0, err
	}
	records = append(retainValidPreflightRecords(records, now.Unix()), record)
	if err := savePreflightRecords(records); err != nil {
		return "", 0, err
	}
	return identifier, record.ExpiresAt, nil
}

// ConsumePreflight 复核并消耗一次性预检记录。
// 过期、重放、来源类型不匹配、记录或上传源变更、散列不一致一律拒绝。
func ConsumePreflight(identifier string, source PreflightSource) (string, error) {
	if identifier == "" {
		return "", ErrPreflightInvalid
	}
	if err := source.validate(); err != nil {
		return "", err
	}

	preflightStoreMutex.Lock()
	defer preflightStoreMutex.Unlock()
	records, err := loadPreflightRecords()
	if err != nil {
		return "", err
	}

	now := time.Now().Unix()
	hashed := sha256Hex([]byte(identifier))
	for index, record := range records {
		if record.IDSHA256 != hashed {
			continue
		}
		if record.Used || record.ExpiresAt <= now ||
			record.Kind != source.Kind || record.RecordID != source.RecordID ||
			record.ArtifactPath != source.ArtifactPath || record.ArtifactSHA256 != source.ArtifactSHA256 {
			return "", ErrPreflightInvalid
		}
		records[index].Used = true
		if err := savePreflightRecords(retainValidPreflightRecords(records, now)); err != nil {
			return "", err
		}
		return record.TargetLabel, nil
	}
	return "", ErrPreflightInvalid
}

// ResolvePreflightSource 返回仍有效且尚未使用的预检来源，供上传确认定位服务端暂存工件。
// 它不消耗记录；ConfirmRestore 会在重新验证工件后原子标记该预检已使用。
// 客户端永远不提交或控制上传工件路径。
func ResolvePreflightSource(identifier string, kind PreflightSourceKind) (PreflightSource, error) {
	if identifier == "" || (kind != PreflightSourceRecord && kind != PreflightSourceUpload) {
		return PreflightSource{}, ErrPreflightInvalid
	}

	preflightStoreMutex.Lock()
	defer preflightStoreMutex.Unlock()
	records, err := loadPreflightRecords()
	if err != nil {
		return PreflightSource{}, err
	}

	now := time.Now().Unix()
	hashed := sha256Hex([]byte(identifier))
	for _, record := range records {
		if record.IDSHA256 != hashed {
			continue
		}
		if record.Used || record.ExpiresAt <= now || record.Kind != kind {
			return PreflightSource{}, ErrPreflightInvalid
		}
		return PreflightSource{
			Kind:           record.Kind,
			RecordID:       record.RecordID,
			ArtifactPath:   record.ArtifactPath,
			ArtifactSHA256: record.ArtifactSHA256,
		}, nil
	}
	return PreflightSource{}, ErrPreflightInvalid
}

// ClearPreflightRecords 使全部预检记录立即失效。
// 定时备份清空上传暂存和进程启动清理时调用，避免失效工件仍可被确认阶段引用。
func ClearPreflightRecords() {
	preflightStoreMutex.Lock()
	defer preflightStoreMutex.Unlock()
	if err := savePreflightRecords(nil); err != nil {
		helpers.AppLogger.Warnf("清理预检记录失败：%v", err)
	}
}

func (source PreflightSource) validate() error {
	if source.Kind != PreflightSourceRecord && source.Kind != PreflightSourceUpload {
		return ErrPreflightInvalid
	}
	if source.ArtifactPath == "" || !filepath.IsAbs(source.ArtifactPath) || !isSHA256(source.ArtifactSHA256) {
		return ErrPreflightInvalid
	}
	if (source.Kind == PreflightSourceRecord) != (source.RecordID > 0) {
		return ErrPreflightInvalid
	}
	return nil
}

// retainValidPreflightRecords 丢弃已使用和已过期的记录，使状态文件不积累历史。
func retainValidPreflightRecords(records []preflightRecord, now int64) []preflightRecord {
	retained := make([]preflightRecord, 0, len(records))
	for _, record := range records {
		if record.Used || record.ExpiresAt <= now {
			continue
		}
		retained = append(retained, record)
	}
	return retained
}

func preflightStatePath() string {
	return filepath.Join(StateDir(), preflightStateFileName)
}

func loadPreflightRecords() ([]preflightRecord, error) {
	file, err := os.Open(preflightStatePath())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取预检记录: %w", err)
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, preflightStateMaxSize+1))
	if err != nil {
		return nil, fmt.Errorf("读取预检记录: %w", err)
	}
	if len(data) > preflightStateMaxSize {
		return nil, ErrPreflightInvalid
	}
	var records []preflightRecord
	if err := decodeStrictJSON(data, &records); err != nil {
		// 无法解析的记录文件只能整体作废，绝不能被当作有效的一次性凭据。
		helpers.AppLogger.Warnf("预检记录文件无效，已整体作废")
		return nil, nil
	}
	return records, nil
}

func savePreflightRecords(records []preflightRecord) error {
	if err := os.MkdirAll(StateDir(), 0o700); err != nil {
		return fmt.Errorf("创建备份状态目录: %w", err)
	}
	data, err := json.Marshal(records)
	if err != nil {
		return fmt.Errorf("编码预检记录: %w", err)
	}
	return replaceFileAtomically(preflightStatePath(), func(output *os.File) error {
		if _, err := output.Write(data); err != nil {
			return fmt.Errorf("写入预检记录: %w", err)
		}
		return nil
	})
}
