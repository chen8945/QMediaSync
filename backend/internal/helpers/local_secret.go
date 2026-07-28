package helpers

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

var localEncryptionKey = ""

const localEncryptionKeyFileName = "encryption.key"
const localSecretGCMPrefix = "gcm:"

// InitEncryptionKey 初始化本机加密密钥，用于保存实例本地敏感数据。
func InitEncryptionKey() error {
	if localEncryptionKey != "" {
		return nil
	}
	if ConfigDir == "" {
		return errors.New("配置目录不能为空")
	}

	keyPath := filepath.Join(ConfigDir, localEncryptionKeyFileName)
	data, err := os.ReadFile(keyPath)
	if err == nil {
		key := strings.TrimSpace(string(data))
		if key == "" {
			return fmt.Errorf("本机加密密钥文件为空：%s", keyPath)
		}
		localEncryptionKey = key
		return nil
	}
	if !os.IsNotExist(err) {
		return fmt.Errorf("读取本机加密密钥失败：%w", err)
	}

	if err := os.MkdirAll(ConfigDir, 0755); err != nil {
		return fmt.Errorf("创建配置目录失败：%w", err)
	}
	keyBytes := make([]byte, 32)
	if _, err := rand.Read(keyBytes); err != nil {
		return fmt.Errorf("生成本机加密密钥失败：%w", err)
	}
	key := base64.RawURLEncoding.EncodeToString(keyBytes)
	if err := os.WriteFile(keyPath, []byte(key+"\n"), 0600); err != nil {
		return fmt.Errorf("保存本机加密密钥失败：%w", err)
	}
	localEncryptionKey = key
	return nil
}

// EncryptLocalSecret 加密实例本地保存的敏感数据。
func EncryptLocalSecret(plaintext string) (string, error) {
	if err := InitEncryptionKey(); err != nil {
		return "", err
	}
	gcm, err := localSecretGCM()
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	ciphertext := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return localSecretGCMPrefix + base64.RawURLEncoding.EncodeToString(ciphertext), nil
}

// DecryptLocalSecret 解密实例本地保存的敏感数据。
func DecryptLocalSecret(encrypted string) (string, error) {
	if err := InitEncryptionKey(); err != nil {
		return "", err
	}
	if !strings.HasPrefix(encrypted, localSecretGCMPrefix) {
		return "", errors.New("本机 secret 格式不支持")
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(encrypted, localSecretGCMPrefix))
	if err != nil {
		return "", err
	}
	gcm, err := localSecretGCM()
	if err != nil {
		return "", err
	}
	if len(payload) < gcm.NonceSize() {
		return "", errors.New("本机 secret 密文长度不足")
	}
	nonce := payload[:gcm.NonceSize()]
	ciphertext := payload[gcm.NonceSize():]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}

// LocalEncryptionKeyText 返回本机加密密钥的有效文本。
// 备份工件以它的 UTF-8 字节散列作为实例指纹，因此这里必须与 InitEncryptionKey 读取到的
// 去空白文本完全一致，不能返回解码后的密钥字节。
func LocalEncryptionKeyText() (string, error) {
	if err := InitEncryptionKey(); err != nil {
		return "", err
	}
	return localEncryptionKey, nil
}

func localSecretGCM() (cipher.AEAD, error) {
	key := localSecretKey()
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func localSecretKey() []byte {
	if key, err := base64.RawURLEncoding.DecodeString(localEncryptionKey); err == nil && len(key) == 32 {
		return key
	}
	keyHash := sha256.Sum256([]byte(localEncryptionKey))
	return keyHash[:]
}
