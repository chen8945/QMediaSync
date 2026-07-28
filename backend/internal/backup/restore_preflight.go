package backup

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
)

// RestorePreflightRequest 是一次无副作用恢复预检的参数。
// Password 只存在于本次处理期间：服务端不得持久化明文、派生密钥或可还原的密码证明。
type RestorePreflightRequest struct {
	Kind         PreflightSourceKind
	RecordID     uint
	ArtifactPath string
	Password     []byte
}

// RestorePreflightResult 是预检成功后返回给恢复页面的非敏感结果。
type RestorePreflightResult struct {
	PreflightID string `json:"preflight_id"`
	ExpiresAt   int64  `json:"expires_at"`
	TargetLabel string `json:"target_label"`
}

// PreflightRestore 完成恢复的第一阶段：密码、格式、完整性、白名单、引擎/schema/实例指纹
// 以及备份配置指定目标的连通性、快照能力和空间验证。
// 它不进入维护、不创建预恢复快照、不写入任何数据库，只登记一次性 preflight_id。
func PreflightRestore(request RestorePreflightRequest) (RestorePreflightResult, error) {
	result, err := runRestorePreflight(request)
	if err != nil && request.Kind == PreflightSourceUpload {
		// 预检失败的上传工件立即删除，避免失败候选长期占用暂存目录。
		if removeErr := os.Remove(request.ArtifactPath); removeErr != nil && !os.IsNotExist(removeErr) {
			helpers.AppLogger.Warnf("删除预检失败的上传工件失败：%v", removeErr)
		}
	}
	return result, err
}

func runRestorePreflight(request RestorePreflightRequest) (RestorePreflightResult, error) {
	verified, artifactSHA256, err := verifyRestoreArtifact(request.ArtifactPath, request.Password)
	if err != nil {
		return RestorePreflightResult{}, err
	}
	defer func() {
		if cleanupErr := verified.Cleanup(); cleanupErr != nil {
			helpers.AppLogger.Warnf("清理预检暂存文件失败：%v", cleanupErr)
		}
	}()

	target, err := resolveVerifiedRestoreTarget(verified)
	if err != nil {
		return RestorePreflightResult{}, err
	}
	if err := checkRestoreTargetReady(target, verified.Header.Encryption.PlaintextSize); err != nil {
		return RestorePreflightResult{}, err
	}

	source := PreflightSource{
		Kind:           request.Kind,
		RecordID:       request.RecordID,
		ArtifactPath:   request.ArtifactPath,
		ArtifactSHA256: artifactSHA256,
	}
	identifier, expiresAt, err := IssuePreflight(source, target.Label)
	if err != nil {
		return RestorePreflightResult{}, err
	}
	return RestorePreflightResult{
		PreflightID: identifier,
		ExpiresAt:   expiresAt,
		TargetLabel: target.Label,
	}, nil
}

// verifyRestoreArtifact 完整验证工件并返回其文件级散列。
// 文件散列是预检与确认之间的来源绑定：确认阶段据此拒绝被替换的记录或上传文件。
func verifyRestoreArtifact(artifactPath string, password []byte) (VerifiedArtifact, string, error) {
	if artifactPath == "" || !filepath.IsAbs(artifactPath) {
		return VerifiedArtifact{}, "", fmt.Errorf("%w：工件路径无效", ErrInvalidArtifact)
	}
	artifactSHA256, err := fileSHA256(artifactPath)
	if err != nil {
		return VerifiedArtifact{}, "", err
	}
	keyText, err := helpers.LocalEncryptionKeyText()
	if err != nil {
		return VerifiedArtifact{}, "", fmt.Errorf("读取实例密钥: %w", err)
	}
	verified, err := VerifyArtifact(ArtifactVerificationOptions{
		ArtifactPath:         artifactPath,
		StagingDir:           VerificationStagingDir(),
		Password:             password,
		CurrentEncryptionKey: []byte(keyText),
	})
	if err != nil {
		return VerifiedArtifact{}, "", err
	}
	return verified, artifactSHA256, nil
}

// resolveVerifiedRestoreTarget 从已验证工件解析恢复目标并校验兼容性。
func resolveVerifiedRestoreTarget(verified VerifiedArtifact) (RestoreTarget, error) {
	reader, err := openInnerArchive(verified.InnerArchivePath, verified.Manifest)
	if err != nil {
		return RestoreTarget{}, err
	}
	defer reader.Close()

	configYAML, err := reader.artifactConfigYAML()
	if err != nil {
		return RestoreTarget{}, err
	}
	target, err := resolveRestoreTarget(configYAML)
	if err != nil {
		return RestoreTarget{}, err
	}
	if err := verifyRestoreCompatibility(verified.Header, target, models.SchemaVersion); err != nil {
		return RestoreTarget{}, err
	}
	return target, nil
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("打开备份工件: %w", err)
	}
	defer file.Close()

	hash := sha256.New()
	if _, err := io.Copy(hash, io.LimitReader(file, artifactMaxUploadSize+1)); err != nil {
		return "", fmt.Errorf("读取备份工件: %w", err)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
