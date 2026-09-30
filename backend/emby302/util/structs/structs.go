package structs

import (
	"fmt"
	"reflect"
	"strings"
)

// String 将结构体转换为可读字符串
func String(s any) string {
	if !IsStruct(s) {
		return fmt.Sprintf("%v", s)
	}
	t := reflect.TypeOf(s)
	v := reflect.ValueOf(s)
	if t.Kind() == reflect.Pointer {
		t, v = t.Elem(), v.Elem()
	}
	sb := strings.Builder{}
	sb.WriteString("{")
	for i := 0; i < t.NumField(); i++ {
		fieldType := t.Field(i)
		val := v.Field(i)
		if IsStruct(val.Interface()) {
			sb.WriteString(fmt.Sprintf("%s: %s", fieldType.Name, String(val.Interface())))
		} else {
			sb.WriteString(fmt.Sprintf("%s: %v", fieldType.Name, val.Interface()))
		}
		if i < t.NumField()-1 {
			sb.WriteString(", ")
		}
	}
	sb.WriteString("}")
	return sb.String()
}

// IsStruct 判断一个变量是否为结构体
func IsStruct(v any) bool {
	if v == nil {
		return false
	}
	t := reflect.TypeOf(v)
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t.Kind() == reflect.Struct
}
