package v115open

import (
	"net/http"
	"net/url"
	"strings"
	"sync"

	"resty.dev/v3"
)

type directoryWriteKey struct {
	accountID   uint
	directoryID string
}
type directoryWriteState struct {
	version uint64
	active  int
}

var directoryWrites = struct {
	sync.Mutex
	sequence uint64
	states   map[directoryWriteKey]directoryWriteState
}{states: make(map[directoryWriteKey]directoryWriteState)}

// DirectoryReadVersion 返回账号内指定目录的写入版本；未知位置的写入影响整个账号。
// 写入失败也改变版本，因为请求可能已经在远端执行。
func DirectoryReadVersion(accountID uint, directoryID string) (uint64, bool) {
	directoryWrites.Lock()
	defer directoryWrites.Unlock()
	account := directoryWrites.states[directoryWriteKey{accountID, ""}]
	directory := directoryWrites.states[directoryWriteKey{accountID, directoryID}]
	return max(account.version, directory.version), account.active == 0 && directory.active == 0
}

func beginDirectoryWrite(accountID uint, directoryID string) func() {
	key := directoryWriteKey{accountID, directoryID}
	update := func(delta int) {
		directoryWrites.Lock()
		defer directoryWrites.Unlock()
		directoryWrites.sequence++
		state := directoryWrites.states[key]
		state.version = directoryWrites.sequence
		state.active += delta
		directoryWrites.states[key] = state
	}
	update(1)
	return func() { update(-1) }
}

// 请求方法不能表示是否修改目录：下载地址和路径详情也是 POST。
func beginRequestDirectoryWrite(accountID uint, rawURL string, req *resty.Request) func() {
	noop := func() {}
	if req.Method == http.MethodGet || req.Method == http.MethodHead {
		return noop
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return beginDirectoryWrite(accountID, "")
	}
	var directoryID string
	switch parsed.Path {
	case "/open/folder/get_info", "/open/ufile/downurl", "/open/video/play", "/open/upload/get_token",
		"/open/authDeviceCode", "/open/deviceCodeToToken", "/open/refreshToken":
		return noop
	case "/open/ufile/copy", "/open/folder/add":
		directoryID = req.FormData.Get("pid")
	case "/open/ufile/delete":
		directoryID = req.FormData.Get("parent_id")
	case "/open/upload/init", "/open/upload/resume":
		if target, ok := strings.CutPrefix(req.FormData.Get("target"), "U_1_"); ok {
			directoryID = target
		}
		// 移动和改名未携带原父目录；未知接口也保守地只使当前账号失效。
	}
	return beginDirectoryWrite(accountID, directoryID)
}
