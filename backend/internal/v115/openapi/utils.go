package v115open

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
)

// GenCodeChallenge 生成代码挑战值
func GenCodeChallenge(codeVerifier string) string {
	sha := sha256.New()
	sha.Write([]byte(codeVerifier))
	hash := sha.Sum(nil)
	hashBase64 := base64.StdEncoding.EncodeToString(hash)
	return hashBase64
}

type StructOrArray[T any] struct {
	Value *T // 正常时解析为对象
	Array []T
}

func (dw *StructOrArray[T]) UnmarshalJSON(data []byte) error {
	// 尝试解析为 DataObject
	var obj T
	if err := json.Unmarshal(data, &obj); err == nil {
		dw.Value = &obj
		return nil
	}

	// 尝试解析为数组
	var arr []T
	if err := json.Unmarshal(data, &arr); err == nil {
		if len(arr) == 0 {
			dw.Value = nil
			return nil
		}
		dw.Array = arr
		return nil
	}

	return fmt.Errorf("data is neither object nor array")
}
