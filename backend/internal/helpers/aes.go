package helpers

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"strings"
)

// AES-256-CBC 加密（URL 安全）
func Encrypt(plaintext string) (string, error) {
	return EncryptWithKey(plaintext, OAuthRelayEncryptionKey)
}

func EncryptWithKey(plaintext string, keyText string) (string, error) {
	if keyText == "" {
		return "", errors.New("加密密钥不能为空")
	}
	keyHash := sha256.Sum256([]byte(keyText))
	key := keyHash[:]

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}

	// PKCS7 填充
	padding := aes.BlockSize - len(plaintext)%aes.BlockSize
	padtext := append([]byte(plaintext), make([]byte, padding)...)
	for i := range padding {
		padtext[len(plaintext)+i] = byte(padding)
	}

	// IV + 密文
	ciphertext := make([]byte, aes.BlockSize+len(padtext))
	iv := ciphertext[:aes.BlockSize]

	if _, err := io.ReadFull(rand.Reader, iv); err != nil {
		return "", err
	}

	mode := cipher.NewCBCEncrypter(block, iv)
	mode.CryptBlocks(ciphertext[aes.BlockSize:], padtext)

	// URL 安全的 Base64 编码
	encoded := base64.StdEncoding.EncodeToString(ciphertext)
	encoded = strings.ReplaceAll(encoded, "+", "-")
	encoded = strings.ReplaceAll(encoded, "/", "_")
	encoded = strings.TrimRight(encoded, "=")

	return encoded, nil
}

// AES-256-CBC 解密（URL 安全）
func Decrypt(encrypted string) (string, error) {
	return DecryptWithKey(encrypted, OAuthRelayEncryptionKey)
}

func DecryptWithKey(encrypted string, keyText string) (string, error) {
	if keyText == "" {
		return "", errors.New("加密密钥不能为空")
	}
	keyHash := sha256.Sum256([]byte(keyText))
	key := keyHash[:]

	// URL 安全的 Base64 解码
	encoded := strings.ReplaceAll(encrypted, "-", "+")
	encoded = strings.ReplaceAll(encoded, "_", "/")
	// 补齐=
	padding := (4 - len(encoded)%4) % 4
	encoded += strings.Repeat("=", padding)

	ciphertext, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", err
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}

	if len(ciphertext) < aes.BlockSize {
		return "", errors.New("ciphertext too short")
	}

	iv := ciphertext[:aes.BlockSize]
	ciphertext = ciphertext[aes.BlockSize:]

	if len(ciphertext)%aes.BlockSize != 0 {
		return "", errors.New("ciphertext is not a multiple of the block size")
	}

	mode := cipher.NewCBCDecrypter(block, iv)
	mode.CryptBlocks(ciphertext, ciphertext)

	// 去除 PKCS7 填充
	paddingLen := int(ciphertext[len(ciphertext)-1])
	if paddingLen > len(ciphertext) || paddingLen > aes.BlockSize {
		return "", errors.New("invalid padding")
	}

	return string(ciphertext[:len(ciphertext)-paddingLen]), nil
}
