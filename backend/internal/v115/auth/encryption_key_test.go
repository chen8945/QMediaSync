package v115auth

import (
	"testing"

	"qmediasync/internal/helpers"
)

func TestSharedEncryptionKeyMissing(t *testing.T) {
	_, err := helpers.EncryptWithKey("{}", "")
	if err == nil {
		t.Fatal("OAUTH_RELAY_ENCRYPTION_KEY 为空时应返回错误")
	}
}

func TestSharedEncryptionKeyRoundTrip(t *testing.T) {
	encrypted, err := helpers.EncryptWithKey(`{"state":"abc"}`, "shared-secret")
	if err != nil {
		t.Fatalf("加密 relay state 失败: %v", err)
	}
	decrypted, err := helpers.DecryptWithKey(encrypted, "shared-secret")
	if err != nil {
		t.Fatalf("解密 relay data 失败: %v", err)
	}
	if decrypted != `{"state":"abc"}` {
		t.Fatalf("解密结果 = %s，期望原始 state", decrypted)
	}
}
