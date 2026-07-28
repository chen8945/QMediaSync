package backup

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"

	"golang.org/x/crypto/argon2"
)

func newArtifactEncryption(password []byte) (ArtifactEncryption, []byte, error) {
	salt := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return ArtifactEncryption{}, nil, fmt.Errorf("生成工件加密盐: %w", err)
	}
	noncePrefix := make([]byte, 4)
	if _, err := io.ReadFull(rand.Reader, noncePrefix); err != nil {
		return ArtifactEncryption{}, nil, fmt.Errorf("生成工件随机前缀: %w", err)
	}
	key := deriveArtifactKey(password, salt)
	return ArtifactEncryption{
		Enabled:       true,
		KDF:           artifactEncryptionKDF,
		MemoryKiB:     artifactKDFMemoryKiB,
		Time:          artifactKDFTime,
		Parallelism:   artifactKDFParallelism,
		Salt:          base64.RawStdEncoding.EncodeToString(salt),
		Cipher:        artifactEncryptionCipher,
		NoncePrefix:   base64.RawStdEncoding.EncodeToString(noncePrefix),
		PlaintextSize: 0,
	}, key, nil
}

func deriveArtifactKey(password []byte, salt []byte) []byte {
	return argon2.IDKey(
		password,
		salt,
		artifactKDFTime,
		artifactKDFMemoryKiB,
		artifactKDFParallelism,
		32,
	)
}

func encryptArtifactPayload(
	destination io.Writer,
	source io.Reader,
	password []byte,
	artifactID string,
) (ArtifactEncryption, int64, error) {
	encryption, key, err := newArtifactEncryption(password)
	if err != nil {
		return ArtifactEncryption{}, 0, err
	}
	defer clear(key)

	block, err := aes.NewCipher(key)
	if err != nil {
		return ArtifactEncryption{}, 0, fmt.Errorf("创建工件加密器: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return ArtifactEncryption{}, 0, fmt.Errorf("创建工件认证加密器: %w", err)
	}
	noncePrefix, err := decodeArtifactEncoding(encryption.NoncePrefix, 4)
	if err != nil {
		return ArtifactEncryption{}, 0, err
	}

	buffer := make([]byte, artifactChunkSize)
	var plaintextSize int64
	for sequence := uint64(0); ; sequence++ {
		count, readErr := io.ReadFull(source, buffer)
		if readErr == io.EOF {
			break
		}
		if readErr != nil && readErr != io.ErrUnexpectedEOF {
			return ArtifactEncryption{}, 0, fmt.Errorf("读取工件明文载荷: %w", readErr)
		}
		if count == 0 {
			break
		}
		if plaintextSize > artifactMaxInnerSize-int64(count) {
			return ArtifactEncryption{}, 0, fmt.Errorf("%w：内层归档超出限制", ErrInvalidArtifact)
		}

		nonce := artifactNonce(noncePrefix, sequence)
		ciphertext := aead.Seal(nil, nonce[:], buffer[:count], artifactChunkAAD(artifactID, sequence))
		if _, err := destination.Write(ciphertext); err != nil {
			return ArtifactEncryption{}, 0, fmt.Errorf("写入加密工件载荷: %w", err)
		}
		plaintextSize += int64(count)
		if readErr == io.ErrUnexpectedEOF {
			break
		}
	}
	encryption.PlaintextSize = plaintextSize
	return encryption, plaintextSize, nil
}

func decryptArtifactPayload(
	destination io.Writer,
	source io.Reader,
	header ArtifactHeader,
	password []byte,
) error {
	if !header.Encryption.Enabled {
		written, err := io.Copy(destination, io.LimitReader(source, header.PayloadSize+1))
		if err != nil {
			return fmt.Errorf("写入未加密工件载荷: %w", err)
		}
		if written != header.PayloadSize || written != header.Encryption.PlaintextSize {
			return fmt.Errorf("%w：未加密载荷长度不匹配", ErrInvalidArtifact)
		}
		return nil
	}
	if len(password) == 0 {
		return ErrArtifactPasswordOrCorrupt
	}

	salt, err := decodeArtifactEncoding(header.Encryption.Salt, 16)
	if err != nil {
		return fmt.Errorf("%w：加密盐无效", ErrInvalidArtifact)
	}
	noncePrefix, err := decodeArtifactEncoding(header.Encryption.NoncePrefix, 4)
	if err != nil {
		return fmt.Errorf("%w：随机前缀无效", ErrInvalidArtifact)
	}
	key := deriveArtifactKey(password, salt)
	defer clear(key)
	block, err := aes.NewCipher(key)
	if err != nil {
		return fmt.Errorf("创建工件解密器: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return fmt.Errorf("创建工件认证解密器: %w", err)
	}

	remaining := header.Encryption.PlaintextSize
	for sequence := uint64(0); remaining > 0; sequence++ {
		plaintextChunkSize := min(remaining, int64(artifactChunkSize))
		ciphertext := make([]byte, plaintextChunkSize+int64(aead.Overhead()))
		if _, err := io.ReadFull(source, ciphertext); err != nil {
			return ErrArtifactPasswordOrCorrupt
		}
		nonce := artifactNonce(noncePrefix, sequence)
		plaintext, err := aead.Open(nil, nonce[:], ciphertext, artifactChunkAAD(header.ArtifactID, sequence))
		if err != nil || int64(len(plaintext)) != plaintextChunkSize {
			return ErrArtifactPasswordOrCorrupt
		}
		if _, err := destination.Write(plaintext); err != nil {
			return fmt.Errorf("写入解密工件载荷: %w", err)
		}
		remaining -= plaintextChunkSize
	}
	var extra [1]byte
	if count, err := source.Read(extra[:]); count != 0 || (err != nil && err != io.EOF) {
		return ErrArtifactPasswordOrCorrupt
	}
	return nil
}

func decodeArtifactEncoding(value string, expectedLength int) ([]byte, error) {
	decoded, err := base64.RawStdEncoding.DecodeString(value)
	if err != nil || len(decoded) != expectedLength {
		return nil, fmt.Errorf("无效的工件编码")
	}
	return decoded, nil
}

func artifactNonce(prefix []byte, sequence uint64) [12]byte {
	var nonce [12]byte
	copy(nonce[:4], prefix)
	binary.BigEndian.PutUint64(nonce[4:], sequence)
	return nonce
}

func artifactChunkAAD(artifactID string, sequence uint64) []byte {
	aad := make([]byte, 0, len("qmediasync-backup-v1")+len(artifactID)+10)
	aad = append(aad, "qmediasync-backup-v1"...)
	aad = append(aad, 0)
	aad = append(aad, artifactID...)
	aad = append(aad, 0)
	var sequenceBytes [8]byte
	binary.BigEndian.PutUint64(sequenceBytes[:], sequence)
	return append(aad, sequenceBytes[:]...)
}
