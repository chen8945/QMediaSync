package jsons

import (
	"net/http"
	"strconv"
)

// OkResp 返回 JSON 响应, 状态码 200
func OkResp(w http.ResponseWriter, data *Item) {
	Resp(w, http.StatusOK, data)
}

// Resp 返回 JSON 响应
func Resp(w http.ResponseWriter, code int, data *Item) {
	if w == nil {
		return
	}

	if data == nil {
		data = NewEmptyObj()
	}

	bytes := data.Bytes()
	w.Header().Set("Content-Length", strconv.Itoa(len(bytes)))
	w.WriteHeader(code)
	w.Write(bytes)
}
