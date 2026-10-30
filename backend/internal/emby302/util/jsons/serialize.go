package jsons

import (
	"bytes"
	"encoding/json"
	"sync"
)

// Struct 将 Item 转换为结构体对象
func (i *Item) Struct() any {
	switch i.jType {
	case JsonTypeVal:
		return i.val
	case JsonTypeObj:
		m := make(map[string]any)
		for key, value := range i.obj {
			m[key] = value.Struct()
		}
		return m
	case JsonTypeArr:
		a := make([]any, len(i.arr))
		for idx, value := range i.arr {
			a[idx] = value.Struct()
		}
		return a
	default:
		return "null"
	}
}

// bufPool JSON 序列化时复用缓冲区
var bufPool = sync.Pool{
	New: func() any {
		return new(bytes.Buffer)
	},
}

func (i *Item) MarshalJSON() ([]byte, error) {
	buf := bufPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer bufPool.Put(buf)

	enc := json.NewEncoder(buf)
	enc.SetEscapeHTML(false)

	switch i.jType {
	case JsonTypeVal:
		err := enc.Encode(i.val)
		return bytes.TrimSpace(buf.Bytes()), err
	case JsonTypeObj:
		err := enc.Encode(i.obj)
		return bytes.TrimSpace(buf.Bytes()), err
	case JsonTypeArr:
		err := enc.Encode(i.arr)
		return bytes.TrimSpace(buf.Bytes()), err
	default:
		return []byte("null"), nil
	}
}

// Bytes 将 Item 转换为字节切片
func (i *Item) Bytes() []byte {
	buf := bufPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer bufPool.Put(buf)

	enc := json.NewEncoder(buf)
	enc.SetEscapeHTML(false)

	enc.Encode(i)
	return append([]byte(nil), buf.Bytes()...)
}

// String 将 Item 转换为 JSON 字符串
func (i *Item) String() string {
	return string(i.Bytes())
}
