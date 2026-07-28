package backup

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sync"

	"gorm.io/gorm/schema"
)

// artifactRecordCodec 按 GORM 的持久化列而不是 API JSON 标签编解码工件记录。
// 这样不对外暴露的密文、哈希等列也会被安全地包含在加密工件中。
type artifactRecordCodec struct {
	modelType reflect.Type
	schema    *schema.Schema
}

func newArtifactRecordCodec(model any) (artifactRecordCodec, error) {
	modelType := reflect.TypeOf(model)
	if modelType == nil {
		return artifactRecordCodec{}, errors.New("模型类型为空")
	}
	for modelType.Kind() == reflect.Ptr {
		modelType = modelType.Elem()
	}
	if modelType.Kind() != reflect.Struct {
		return artifactRecordCodec{}, fmt.Errorf("模型类型必须是结构体，实际为 %s", modelType.Kind())
	}
	modelSchema, err := schema.Parse(model, &sync.Map{}, schema.NamingStrategy{})
	if err != nil {
		return artifactRecordCodec{}, err
	}
	return artifactRecordCodec{modelType: modelType, schema: modelSchema}, nil
}

func (codec artifactRecordCodec) recordMap(record reflect.Value) map[string]any {
	values := make(map[string]any, len(codec.schema.Fields))
	for _, field := range codec.schema.Fields {
		if field.DBName == "" {
			continue
		}
		value, _ := field.ValueOf(context.Background(), record)
		values[field.DBName] = value
	}
	return values
}

func (codec artifactRecordCodec) unmarshalRecord(line []byte) (reflect.Value, error) {
	decoder := json.NewDecoder(bytes.NewReader(line))
	var values map[string]json.RawMessage
	if err := decoder.Decode(&values); err != nil {
		return reflect.Value{}, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return reflect.Value{}, errors.New("JSONL 包含多个值")
	}
	if values == nil {
		return reflect.Value{}, errors.New("JSONL 记录必须是对象")
	}

	record := reflect.New(codec.modelType)
	for _, field := range codec.schema.Fields {
		if field.DBName == "" {
			continue
		}
		raw, exists := values[field.DBName]
		if !exists {
			return reflect.Value{}, fmt.Errorf("缺少列 %s", field.DBName)
		}
		destination := field.ReflectValueOf(context.Background(), record)
		if !destination.CanAddr() {
			return reflect.Value{}, fmt.Errorf("无法写入列 %s", field.DBName)
		}
		if err := json.Unmarshal(raw, destination.Addr().Interface()); err != nil {
			return reflect.Value{}, fmt.Errorf("解析列 %s: %w", field.DBName, err)
		}
		delete(values, field.DBName)
	}
	if len(values) != 0 {
		return reflect.Value{}, errors.New("JSONL 包含未知列")
	}
	return record, nil
}

func (codec artifactRecordCodec) verifyJSONLines(reader io.Reader) (int64, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), artifactMaxJSONLineSize+1)
	var recordCount int64
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 || len(line) > artifactMaxJSONLineSize {
			return 0, fmt.Errorf("%w：JSONL 内容无效", ErrInvalidArtifact)
		}
		if _, err := codec.unmarshalRecord(line); err != nil {
			return 0, fmt.Errorf("%w：JSONL 记录无效：%w", ErrInvalidArtifact, err)
		}
		recordCount++
	}
	if err := scanner.Err(); err != nil {
		return 0, fmt.Errorf("%w：JSONL 行超出限制或损坏", ErrInvalidArtifact)
	}
	return recordCount, nil
}
