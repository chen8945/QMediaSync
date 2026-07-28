package backup

import (
	"archive/zip"
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// BuildArtifact 将内层 ZIP 包装为已完整写入并原子发布的 v1 工件。
func BuildArtifact(options ArtifactBuildOptions) (ArtifactHeader, error) {
	defer clear(options.Password)
	if options.Destination == "" || options.InnerArchivePath == "" {
		return ArtifactHeader{}, fmt.Errorf("%w：工件路径不能为空", ErrInvalidArtifact)
	}
	if len(bytes.TrimSpace(options.EncryptionKey)) == 0 {
		return ArtifactHeader{}, fmt.Errorf("%w：实例密钥不能为空", ErrInvalidArtifact)
	}
	if !isArtifactID(options.ArtifactID) || options.CreatedAt <= 0 {
		return ArtifactHeader{}, fmt.Errorf("%w：工件元数据无效", ErrInvalidArtifact)
	}
	innerInfo, err := os.Lstat(options.InnerArchivePath)
	if err != nil {
		return ArtifactHeader{}, fmt.Errorf("读取内层归档: %w", err)
	}
	if !innerInfo.Mode().IsRegular() || innerInfo.Mode()&os.ModeSymlink != 0 ||
		innerInfo.Size() > maxArtifactPlaintextSize(len(options.Password) > 0) {
		return ArtifactHeader{}, fmt.Errorf("%w：内层归档无效", ErrInvalidArtifact)
	}
	innerHeader := ArtifactHeader{
		FormatVersion:            ArtifactFormatVersion,
		ArtifactID:               options.ArtifactID,
		ApplicationVersion:       options.ApplicationVersion,
		SchemaVersion:            options.SchemaVersion,
		SourceEngine:             options.SourceEngine,
		EncryptionKeyFingerprint: EncryptionKeyFingerprint(options.EncryptionKey),
		TableCatalogVersion:      ArtifactTableCatalogVersion,
	}
	if _, err := validateInnerArchive(options.InnerArchivePath, innerHeader, options.EncryptionKey); err != nil {
		return ArtifactHeader{}, err
	}

	payload, err := createArtifactPayload(
		filepath.Dir(options.Destination),
		options.InnerArchivePath,
		options.ArtifactID,
		options.Password,
	)
	if err != nil {
		return ArtifactHeader{}, err
	}
	defer os.Remove(payload.Path)

	header := ArtifactHeader{
		FormatVersion:            ArtifactFormatVersion,
		ArtifactID:               options.ArtifactID,
		CreatedAt:                options.CreatedAt,
		ApplicationVersion:       options.ApplicationVersion,
		SchemaVersion:            options.SchemaVersion,
		SourceEngine:             options.SourceEngine,
		EncryptionKeyFingerprint: EncryptionKeyFingerprint(options.EncryptionKey),
		PayloadSize:              payload.Size,
		PayloadSHA256:            payload.SHA256,
		TableCatalogVersion:      ArtifactTableCatalogVersion,
		Encryption:               payload.Encryption,
	}
	if err := header.validate(); err != nil {
		return ArtifactHeader{}, err
	}
	if err := writeOuterArtifact(options.Destination, header, payload.Path); err != nil {
		return ArtifactHeader{}, err
	}
	return header, nil
}

type artifactPayload struct {
	Path       string
	Size       int64
	SHA256     string
	Encryption ArtifactEncryption
}

func createArtifactPayload(directory string, innerArchivePath string, artifactID string, password []byte) (artifactPayload, error) {
	input, err := os.Open(innerArchivePath)
	if err != nil {
		return artifactPayload{}, fmt.Errorf("打开内层归档: %w", err)
	}
	defer input.Close()

	temporary, err := createArtifactTempFile(directory, ".payload-*")
	if err != nil {
		return artifactPayload{}, err
	}
	temporaryPath := temporary.Name()
	success := false
	defer func() {
		if !success {
			temporary.Close()
			os.Remove(temporaryPath)
		}
	}()

	hash := sha256.New()
	output := io.MultiWriter(temporary, hash)
	var plaintextSize int64
	encryption := ArtifactEncryption{Enabled: false}
	if len(password) == 0 {
		plaintextSize, err = io.Copy(output, io.LimitReader(input, artifactMaxInnerSize+1))
		if err != nil {
			return artifactPayload{}, fmt.Errorf("写入未加密工件载荷: %w", err)
		}
		if plaintextSize > artifactMaxInnerSize {
			return artifactPayload{}, fmt.Errorf("%w：内层归档超出限制", ErrInvalidArtifact)
		}
		encryption.PlaintextSize = plaintextSize
	} else {
		encryption, plaintextSize, err = encryptArtifactPayload(output, input, password, artifactID)
		if err != nil {
			return artifactPayload{}, err
		}
	}
	if err := temporary.Sync(); err != nil {
		return artifactPayload{}, fmt.Errorf("同步工件载荷: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return artifactPayload{}, fmt.Errorf("关闭工件载荷: %w", err)
	}
	info, err := os.Stat(temporaryPath)
	if err != nil {
		return artifactPayload{}, fmt.Errorf("读取工件载荷状态: %w", err)
	}
	if info.Size() != encryptedPayloadSize(plaintextSize) && encryption.Enabled {
		return artifactPayload{}, fmt.Errorf("%w：加密载荷长度不匹配", ErrInvalidArtifact)
	}
	if !encryption.Enabled && info.Size() != plaintextSize {
		return artifactPayload{}, fmt.Errorf("%w：未加密载荷长度不匹配", ErrInvalidArtifact)
	}
	success = true
	return artifactPayload{
		Path:       temporaryPath,
		Size:       info.Size(),
		SHA256:     hex.EncodeToString(hash.Sum(nil)),
		Encryption: encryption,
	}, nil
}

func writeOuterArtifact(destination string, header ArtifactHeader, payloadPath string) error {
	headerJSON, err := json.Marshal(header)
	if err != nil {
		return fmt.Errorf("编码工件头部: %w", err)
	}
	return writeArtifactAtomically(destination, func(output *os.File) error {
		writer := zip.NewWriter(output)
		if err := writeZipEntry(writer, artifactHeaderPath, zip.Store, bytes.NewReader(headerJSON)); err != nil {
			writer.Close()
			return err
		}
		payload, err := os.Open(payloadPath)
		if err != nil {
			writer.Close()
			return fmt.Errorf("打开工件载荷: %w", err)
		}
		if err := writeZipEntry(writer, artifactPayloadPath, zip.Store, payload); err != nil {
			payload.Close()
			writer.Close()
			return err
		}
		if err := payload.Close(); err != nil {
			writer.Close()
			return fmt.Errorf("关闭工件载荷: %w", err)
		}
		if err := writer.Close(); err != nil {
			return fmt.Errorf("关闭工件 ZIP: %w", err)
		}
		return nil
	})
}

func newArtifactID() (string, error) {
	identifier := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, identifier); err != nil {
		return "", fmt.Errorf("生成工件标识: %w", err)
	}
	return hex.EncodeToString(identifier), nil
}
