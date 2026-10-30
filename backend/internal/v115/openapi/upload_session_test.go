package v115open

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"testing"
	"time"
)

func TestParseSignCheckRange(t *testing.T) {
	tests := []struct {
		name      string
		value     string
		wantStart int64
		wantEnd   int64
		wantErr   bool
	}{
		{name: "合法闭区间", value: "0-131071", wantStart: 0, wantEnd: 131071},
		{name: "非法格式", value: "0:131071", wantErr: true},
		{name: "结束位置小于起始位置", value: "100-99", wantErr: true},
		{name: "非数字", value: "a-99", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseSignCheckRange(tt.value)
			if tt.wantErr {
				if err == nil {
					t.Fatal("期望解析失败，实际返回 nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("解析 sign_check 失败：%v", err)
			}
			if got.Start != tt.wantStart || got.End != tt.wantEnd {
				t.Fatalf("range = %+v，期望 %d-%d", got, tt.wantStart, tt.wantEnd)
			}
		})
	}
}

func TestBuildOSSCallbackHeaders(t *testing.T) {
	tests := []struct {
		name        string
		callback    string
		callbackVar string
		wantErr     bool
	}{
		{
			name:        "官方 callback 原样编码且不注入非 x 字段",
			callback:    `{"callbackUrl":"http:\/\/uplb.115.com\/3.0\/completeupload.php","callbackBody":"bucket=${bucket}&object=${object}&size=${size}&sha1=${sha1}&pick_code=${x:pick_code}&user_id=${x:user_id}&behavior_type=${x:behavior_type}&source=${x:source}&target=${x:target}&task_uid=${x:task_uid}"}`,
			callbackVar: `{"x:pick_code":"6a4953ee0de1972b7779ee44","x:user_id":"335314319","x:behavior_type":"0","x:source":"100","x:target":"U_1_3465929644904023372","x:task_uid":"335314319"}`,
		},
		{
			name:        "callback 必须是 JSON 对象",
			callback:    `[]`,
			callbackVar: `{}`,
			wantErr:     true,
		},
		{
			name:        "callback_var 必须是 JSON 对象",
			callback:    `{}`,
			callbackVar: `[]`,
			wantErr:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			headers, err := BuildOSSCallbackHeaders(OSSCallbackInput{
				Callback:    tt.callback,
				CallbackVar: tt.callbackVar,
			})
			if tt.wantErr {
				if err == nil {
					t.Fatal("期望构造 callback header 失败，实际返回 nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("构造 callback header 失败：%v", err)
			}

			callbackBytes, err := base64.StdEncoding.DecodeString(headers.Callback)
			if err != nil {
				t.Fatalf("解码 callback 失败：%v", err)
			}
			if string(callbackBytes) != tt.callback {
				t.Fatalf("callback = %s，期望原样保留 %s", callbackBytes, tt.callback)
			}
			callbackVarBytes, err := base64.StdEncoding.DecodeString(headers.CallbackVar)
			if err != nil {
				t.Fatalf("解码 callback_var 失败：%v", err)
			}
			if string(callbackVarBytes) != tt.callbackVar {
				t.Fatalf("callback_var = %s，期望原样保留 %s", callbackVarBytes, tt.callbackVar)
			}
			callbackVarJSON := decodeBase64JSON(t, headers.CallbackVar)
			for _, key := range []string{"bucket", "object", "size", "sha1"} {
				if _, ok := callbackVarJSON[key]; ok {
					t.Fatalf("callback_var 不应注入 %s：%v", key, callbackVarJSON[key])
				}
			}
		})
	}
}

func TestDecodeUploadCallbackSupportsOfficialArray(t *testing.T) {
	got, err := decodeUploadCallback(json.RawMessage(`[{"callback":"{\"callbackUrl\":\"https://callback.example/upload\"}","callback_var":"{\"x:keep\":\"yes\"}"}]`))
	if err != nil {
		t.Fatalf("解析官方 callback 数组失败：%v", err)
	}
	if got.Callback != `{"callbackUrl":"https://callback.example/upload"}` {
		t.Fatalf("callback = %s", got.Callback)
	}
	if got.CallbackVar != `{"x:keep":"yes"}` {
		t.Fatalf("callback_var = %s", got.CallbackVar)
	}
}

func TestParseCompleteCallbackResult(t *testing.T) {
	tests := []struct {
		name    string
		input   map[string]any
		want    UploadCompleteResult
		wantErr bool
	}{
		{
			name: "合法回调结果",
			input: map[string]any{
				"state":   true,
				"message": "",
				"data": map[string]any{
					"file_id":   "file-1",
					"pick_code": "pick-1",
					"parent_id": "100",
					"sha1":      "SHA1",
					"size":      float64(1024),
					"mtime":     float64(123456),
				},
			},
			want: UploadCompleteResult{
				FileId:   "file-1",
				PickCode: "pick-1",
				ParentId: "100",
				Sha1:     "SHA1",
				Size:     1024,
				Mtime:    123456,
			},
		},
		{
			name: "callback state false 时失败",
			input: map[string]any{
				"state":   false,
				"message": "callback failed",
			},
			wantErr: true,
		},
		{
			name: "缺少 file_id 时失败",
			input: map[string]any{
				"state":   true,
				"message": "",
				"data": map[string]any{
					"pick_code": "pick-1",
				},
			},
			wantErr: true,
		},
		{
			name: "缺少 pick_code 时失败",
			input: map[string]any{
				"state":   true,
				"message": "",
				"data": map[string]any{
					"file_id": "file-1",
				},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseCompleteCallbackResult(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatal("期望解析失败，实际返回 nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("解析 complete callback 失败：%v", err)
			}
			if got != tt.want {
				t.Fatalf("结果 = %+v，期望 %+v", got, tt.want)
			}
		})
	}
}

func TestOpenClient_UploadResumeUsesOfficialFields(t *testing.T) {
	transport := newCaptureOpenAPITransport(`{"state":true,"code":0,"data":{"pick_code":"pick-2","target":"U_1_100","version":"1","bucket":"bucket-1","object":"object-1","callback":{"callback":"{}","callback_var":"{}"}}}`)
	client := newTestOpenClient(transport)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	got, err := client.UploadResume(ctx, "pick-1", 5335, "100", "SHA1")
	if err != nil {
		t.Fatalf("调用断点续传失败：%v", err)
	}
	if got.PickCode != "pick-2" || got.Bucket != "bucket-1" || got.Object != "object-1" {
		t.Fatalf("断点续传结果 = %+v", got)
	}

	req := receiveCapturedRequest(t, transport)
	if req.Method != http.MethodPost {
		t.Fatalf("请求方法 = %s，want %s", req.Method, http.MethodPost)
	}
	if gotPath := mustParseURLPath(t, req.URL); gotPath != "/open/upload/resume" {
		t.Fatalf("请求路径 = %s，want /open/upload/resume", gotPath)
	}
	form, err := url.ParseQuery(req.Body)
	if err != nil {
		t.Fatalf("解析请求表单失败：%v", err)
	}
	for key, want := range map[string]string{
		"file_size": "5335",
		"target":    "U_1_100",
		"fileid":    "SHA1",
		"pick_code": "pick-1",
	} {
		if gotValue := form.Get(key); gotValue != want {
			t.Fatalf("form[%s] = %s，期望 %s", key, gotValue, want)
		}
	}
}

func TestOpenClient_UploadResumeReturnsOpenAPIError(t *testing.T) {
	transport := newCaptureOpenAPITransport(`{"state":false,"code":990001,"message":"resume failed","data":{}}`)
	client := newTestOpenClient(transport)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	_, err := client.UploadResume(ctx, "pick-1", 5335, "100", "SHA1")
	if err == nil {
		t.Fatal("期望断点续传返回错误，实际为 nil")
	}
}

func TestWaitForRapidUpload(t *testing.T) {
	first := &UploadInitResult{Status: UploadInitStatusNeedUpload, PickCode: "pick-1"}

	t.Run("等待命中秒传", func(t *testing.T) {
		calls := 0
		outcome, err := WaitForRapidUpload(
			context.Background(),
			first,
			RapidUploadWaitPolicy{Enabled: true, Timeout: 2 * time.Second, Interval: time.Second},
			1024,
			func(context.Context) (*UploadInitResult, error) {
				calls++
				if calls == 1 {
					return &UploadInitResult{Status: UploadInitStatusNeedUpload, PickCode: "pick-2"}, nil
				}
				return &UploadInitResult{Status: UploadInitStatusRapidUploaded, FileId: "file-1"}, nil
			},
			func(context.Context, time.Duration) error { return nil },
		)
		if err != nil {
			t.Fatalf("等待秒传失败：%v", err)
		}
		if outcome.Result.FileId != "file-1" || outcome.Attempts != 2 || outcome.TimedOut {
			t.Fatalf("等待结果 = %+v", outcome)
		}
	})

	t.Run("小于最小大小时不等待", func(t *testing.T) {
		calls := 0
		outcome, err := WaitForRapidUpload(
			context.Background(),
			first,
			RapidUploadWaitPolicy{Enabled: true, Timeout: 2 * time.Second, Interval: time.Second, MinSize: 10 * 1024},
			1024,
			func(context.Context) (*UploadInitResult, error) {
				calls++
				return &UploadInitResult{Status: UploadInitStatusRapidUploaded, FileId: "file-1"}, nil
			},
			func(context.Context, time.Duration) error { return nil },
		)
		if err != nil {
			t.Fatalf("等待秒传失败：%v", err)
		}
		if calls != 0 || outcome.Result != first || outcome.Attempts != 0 {
			t.Fatalf("不应等待，calls=%d outcome=%+v", calls, outcome)
		}
	})

	t.Run("超时后按配置跳过真实上传", func(t *testing.T) {
		outcome, err := WaitForRapidUpload(
			context.Background(),
			first,
			RapidUploadWaitPolicy{Enabled: true, Timeout: time.Second, Interval: time.Second, SkipUpload: true},
			1024,
			func(context.Context) (*UploadInitResult, error) {
				return &UploadInitResult{Status: UploadInitStatusNeedUpload, PickCode: "pick-2"}, nil
			},
			func(context.Context, time.Duration) error { return nil },
		)
		if err != nil {
			t.Fatalf("等待秒传失败：%v", err)
		}
		if !outcome.TimedOut || !outcome.SkipUpload || outcome.Attempts != 1 {
			t.Fatalf("超时结果 = %+v", outcome)
		}
	})

	t.Run("等待时长不超过剩余超时", func(t *testing.T) {
		var sleeps []time.Duration
		outcome, err := WaitForRapidUpload(
			context.Background(),
			first,
			RapidUploadWaitPolicy{Enabled: true, Timeout: 1500 * time.Millisecond, Interval: time.Second},
			1024,
			func(context.Context) (*UploadInitResult, error) {
				return &UploadInitResult{Status: UploadInitStatusNeedUpload, PickCode: "pick-2"}, nil
			},
			func(_ context.Context, d time.Duration) error {
				sleeps = append(sleeps, d)
				return nil
			},
		)
		if err != nil {
			t.Fatalf("等待秒传失败：%v", err)
		}
		if !outcome.TimedOut || outcome.Attempts != 2 {
			t.Fatalf("超时结果 = %+v", outcome)
		}
		want := []time.Duration{time.Second, 500 * time.Millisecond}
		if len(sleeps) != len(want) {
			t.Fatalf("sleep 次数 = %d，期望 %d，sleeps=%v", len(sleeps), len(want), sleeps)
		}
		for i := range want {
			if sleeps[i] != want[i] {
				t.Fatalf("第 %d 次 sleep = %s，期望 %s，sleeps=%v", i+1, sleeps[i], want[i], sleeps)
			}
		}
	})
}

func decodeBase64JSON(t *testing.T, value string) map[string]string {
	t.Helper()

	raw, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		t.Fatalf("base64 解码失败：%v", err)
	}
	var data map[string]string
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatalf("JSON 解码失败：%v", err)
	}
	return data
}
