package backup

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"strings"

	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
)

const (
	// ArtifactFormatVersion 标识当前备份工件的外层和内层协议版本。
	ArtifactFormatVersion = 1
	// ArtifactTableCatalogVersion 标识工件中的稳定表目录版本。
	ArtifactTableCatalogVersion = 1

	artifactHeaderPath   = "header.json"
	artifactPayloadPath  = "payload.bin"
	artifactManifestPath = "manifest.json"

	artifactMaxUploadSize    = int64(1 << 30)
	artifactMaxPayloadSize   = artifactMaxUploadSize - artifactMaxManifestSize - 1024
	artifactMaxInnerSize     = int64(4 << 30)
	artifactMaxJSONLSize     = int64(1 << 30)
	artifactMaxJSONLineSize  = 16 << 20
	artifactMaxManifestSize  = 1 << 20
	artifactChunkSize        = 1 << 20
	artifactEncryptionKDF    = "argon2id"
	artifactEncryptionCipher = "aes-256-gcm"
	artifactKDFMemoryKiB     = 64 * 1024
	artifactKDFTime          = 3
	artifactKDFParallelism   = 4
)

var (
	// ErrInvalidArtifact 表示工件不符合 v1 格式、完整性或资源限制。
	ErrInvalidArtifact = errors.New("无效的备份工件")
	// ErrArtifactPasswordOrCorrupt 统一表示密码错误或加密载荷无法认证。
	ErrArtifactPasswordOrCorrupt = errors.New("密码错误或工件损坏")
)

// ArtifactHeader 描述外层 ZIP 中不含业务数据和秘密的工件元数据。
type ArtifactHeader struct {
	FormatVersion            int                `json:"format_version"`
	ArtifactID               string             `json:"artifact_id"`
	CreatedAt                int64              `json:"created_at"`
	ApplicationVersion       string             `json:"application_version"`
	SchemaVersion            int                `json:"schema_version"`
	SourceEngine             string             `json:"source_engine"`
	EncryptionKeyFingerprint string             `json:"encryption_key_fingerprint"`
	PayloadSize              int64              `json:"payload_size"`
	PayloadSHA256            string             `json:"payload_sha256"`
	TableCatalogVersion      int                `json:"table_catalog_version"`
	Encryption               ArtifactEncryption `json:"encryption"`
}

// ArtifactEncryption 描述 payload.bin 的固定加密参数。
type ArtifactEncryption struct {
	Enabled       bool   `json:"enabled"`
	KDF           string `json:"kdf,omitempty"`
	MemoryKiB     uint32 `json:"memory_kib,omitempty"`
	Time          uint32 `json:"time,omitempty"`
	Parallelism   uint8  `json:"parallelism,omitempty"`
	Salt          string `json:"salt,omitempty"`
	Cipher        string `json:"cipher,omitempty"`
	NoncePrefix   string `json:"nonce_prefix,omitempty"`
	PlaintextSize int64  `json:"plaintext_size"`
}

// ArtifactManifest 描述内层 ZIP 中数据和白名单文件的完整性元数据。
type ArtifactManifest struct {
	FormatVersion       int                    `json:"format_version"`
	ArtifactID          string                 `json:"artifact_id"`
	ApplicationVersion  string                 `json:"application_version"`
	SchemaVersion       int                    `json:"schema_version"`
	SourceEngine        string                 `json:"source_engine"`
	TableCatalogVersion int                    `json:"table_catalog_version"`
	Files               []ArtifactManifestFile `json:"files"`
}

// ArtifactManifestFile 记录内层 ZIP 一个数据或白名单文件的身份。
type ArtifactManifestFile struct {
	Path        string `json:"path"`
	Size        int64  `json:"size"`
	SHA256      string `json:"sha256"`
	RecordCount int64  `json:"record_count"`
}

// ArtifactFileSource 指定写入内层 ZIP 的一个已暂存常规文件。
// ArchivePath 是工件内路径，SourcePath 是本机暂存路径。
type ArtifactFileSource struct {
	ArchivePath string
	SourcePath  string
	RecordCount int64
}

// ArtifactBuildOptions 定义从内层 ZIP 发布 v1 工件时必须提供的元数据。
type ArtifactBuildOptions struct {
	Destination        string
	InnerArchivePath   string
	ArtifactID         string
	CreatedAt          int64
	ApplicationVersion string
	SchemaVersion      int
	SourceEngine       string
	EncryptionKey      []byte
	Password           []byte
}

// EncryptionKeyFingerprint 返回按配置密钥读取语义规范化后的 SHA-256 指纹。
func EncryptionKeyFingerprint(key []byte) string {
	return sha256Hex(bytes.TrimSpace(key))
}

func sha256Hex(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func (header ArtifactHeader) validate() error {
	if header.FormatVersion != ArtifactFormatVersion {
		return fmt.Errorf("%w：不支持的格式版本", ErrInvalidArtifact)
	}
	if !isArtifactID(header.ArtifactID) {
		return fmt.Errorf("%w：工件标识无效", ErrInvalidArtifact)
	}
	if header.CreatedAt <= 0 || header.ApplicationVersion == "" || header.SchemaVersion < 0 {
		return fmt.Errorf("%w：工件兼容信息无效", ErrInvalidArtifact)
	}
	if !isSupportedEngine(header.SourceEngine) {
		return fmt.Errorf("%w：源数据库引擎无效", ErrInvalidArtifact)
	}
	if !isSHA256(header.EncryptionKeyFingerprint) || !isSHA256(header.PayloadSHA256) {
		return fmt.Errorf("%w：工件摘要无效", ErrInvalidArtifact)
	}
	if header.PayloadSize < 0 || header.PayloadSize > maxArtifactPayloadSize() {
		return fmt.Errorf("%w：载荷大小超出限制", ErrInvalidArtifact)
	}
	if header.TableCatalogVersion != ArtifactTableCatalogVersion {
		return fmt.Errorf("%w：表目录版本不受支持", ErrInvalidArtifact)
	}
	return header.Encryption.validate(header.PayloadSize)
}

func (encryption ArtifactEncryption) validate(payloadSize int64) error {
	if encryption.PlaintextSize < 0 || encryption.PlaintextSize > artifactMaxInnerSize {
		return fmt.Errorf("%w：明文载荷大小超出限制", ErrInvalidArtifact)
	}
	if !encryption.Enabled {
		if encryption.KDF != "" || encryption.MemoryKiB != 0 || encryption.Time != 0 ||
			encryption.Parallelism != 0 || encryption.Salt != "" || encryption.Cipher != "" || encryption.NoncePrefix != "" ||
			encryption.PlaintextSize != payloadSize {
			return fmt.Errorf("%w：未加密载荷参数无效", ErrInvalidArtifact)
		}
		return nil
	}
	if encryption.KDF != artifactEncryptionKDF || encryption.MemoryKiB != artifactKDFMemoryKiB ||
		encryption.Time != artifactKDFTime || encryption.Parallelism != artifactKDFParallelism ||
		encryption.Cipher != artifactEncryptionCipher {
		return fmt.Errorf("%w：加密参数不受支持", ErrInvalidArtifact)
	}
	if _, err := decodeArtifactEncoding(encryption.Salt, 16); err != nil {
		return fmt.Errorf("%w：加密盐无效", ErrInvalidArtifact)
	}
	if _, err := decodeArtifactEncoding(encryption.NoncePrefix, 4); err != nil {
		return fmt.Errorf("%w：随机前缀无效", ErrInvalidArtifact)
	}
	if payloadSize != encryptedPayloadSize(encryption.PlaintextSize) {
		return fmt.Errorf("%w：加密载荷长度不匹配", ErrInvalidArtifact)
	}
	return nil
}

func (manifest ArtifactManifest) validateAgainstHeader(header ArtifactHeader) error {
	if manifest.FormatVersion != header.FormatVersion || manifest.ArtifactID != header.ArtifactID ||
		manifest.ApplicationVersion != header.ApplicationVersion || manifest.SchemaVersion != header.SchemaVersion ||
		manifest.SourceEngine != header.SourceEngine || manifest.TableCatalogVersion != header.TableCatalogVersion {
		return fmt.Errorf("%w：内外层元数据不一致", ErrInvalidArtifact)
	}
	if len(manifest.Files) == 0 {
		return fmt.Errorf("%w：清单不能为空", ErrInvalidArtifact)
	}

	expectedData := make(map[string]struct{}, len(models.RegularBackupRestoreTableCatalog()))
	for _, entry := range models.RegularBackupRestoreTableCatalog() {
		expectedData["data/"+entry.ID+".jsonl"] = struct{}{}
	}

	seen := make(map[string]struct{}, len(manifest.Files))
	hasEncryptionKey := false
	for _, file := range manifest.Files {
		if err := file.validate(); err != nil {
			return err
		}
		if _, exists := seen[file.Path]; exists {
			return fmt.Errorf("%w：清单包含重复文件", ErrInvalidArtifact)
		}
		seen[file.Path] = struct{}{}

		if _, isData := expectedData[file.Path]; isData {
			delete(expectedData, file.Path)
			continue
		}
		if !isAllowedConfigPath(file.Path) {
			return fmt.Errorf("%w：清单包含未授权文件", ErrInvalidArtifact)
		}
		if file.Path == "config/encryption.key" {
			hasEncryptionKey = true
		}
	}
	if len(expectedData) != 0 || !hasEncryptionKey {
		return fmt.Errorf("%w：清单缺少必需文件", ErrInvalidArtifact)
	}
	return nil
}

func (file ArtifactManifestFile) validate() error {
	if err := validateArtifactArchivePath(file.Path); err != nil {
		return err
	}
	if file.Size < 0 || file.Size > artifactMaxInnerSize || file.RecordCount < 0 || !isSHA256(file.SHA256) {
		return fmt.Errorf("%w：清单文件元数据无效", ErrInvalidArtifact)
	}
	if strings.HasPrefix(file.Path, "data/") && file.Size > artifactMaxJSONLSize {
		return fmt.Errorf("%w：JSONL 文件超出限制", ErrInvalidArtifact)
	}
	return nil
}

func validateArtifactArchivePath(archivePath string) error {
	if archivePath == "" || strings.Contains(archivePath, "\\") || path.IsAbs(archivePath) || path.Clean(archivePath) != archivePath {
		return fmt.Errorf("%w：工件路径无效", ErrInvalidArtifact)
	}
	if archivePath == artifactManifestPath || strings.HasPrefix(archivePath, "../") || archivePath == ".." {
		return fmt.Errorf("%w：工件路径无效", ErrInvalidArtifact)
	}
	return nil
}

func isAllowedConfigPath(archivePath string) bool {
	switch archivePath {
	case "config/config.yaml", "config/config.yml", "config/encryption.key", "config/server.crt", "config/server.key":
		return true
	}
	return strings.HasPrefix(archivePath, "config/logs/") && strings.TrimPrefix(archivePath, "config/logs/") != ""
}

func isSupportedEngine(engine string) bool {
	return engine == string(helpers.DbEngineSqlite) || engine == string(helpers.DbEnginePostgres)
}

func isSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func isArtifactID(value string) bool {
	if len(value) != 32 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 16
}

func maxArtifactPayloadSize() int64 {
	return artifactMaxPayloadSize
}

func maxArtifactPlaintextSize(encrypted bool) int64 {
	if !encrypted {
		return artifactMaxPayloadSize
	}

	low, high := int64(0), artifactMaxPayloadSize
	for low < high {
		middle := low + (high-low+1)/2
		if encryptedPayloadSize(middle) <= artifactMaxPayloadSize {
			low = middle
		} else {
			high = middle - 1
		}
	}
	return low
}

func encryptedPayloadSize(plaintextSize int64) int64 {
	if plaintextSize == 0 {
		return 0
	}
	blockCount := (plaintextSize + artifactChunkSize - 1) / artifactChunkSize
	return plaintextSize + blockCount*16
}
