package model

// HttpRes 通用 HTTP 请求结果
type HttpRes[T any] struct {
	Code int
	Data T
	Msg  string
}
