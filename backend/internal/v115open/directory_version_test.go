package v115open

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"
)

func TestDirectoryReadVersionWrites(t *testing.T) {
	before, idle := DirectoryReadVersion(1, "2")
	if !idle {
		t.Fatal("初始仍有写入")
	}
	finish := beginDirectoryWrite(1, "2")
	during, idle := DirectoryReadVersion(1, "2")
	if idle || during == before {
		t.Fatal("写入未阻止复用")
	}
	finishSecond := beginDirectoryWrite(1, "2")
	finish()
	if _, idle := DirectoryReadVersion(1, "2"); idle {
		t.Fatal("第二个写入尚未结束")
	}
	finishSecond()
	after, idle := DirectoryReadVersion(1, "2")
	if !idle || after == during {
		t.Fatal("写入结束未更新版本")
	}
	client := NewClient(1, "", "", "")
	_, _ = client.Move(context.Background(), []string{"1"}, "2")
	failed, idle := DirectoryReadVersion(1, "2")
	if !idle || failed == after {
		t.Fatal("失败的修改没有使列表失效")
	}
	_, _ = client.GetFsDetailByCid(context.Background(), "1")
	read, _ := DirectoryReadVersion(1, "2")
	if read != failed {
		t.Fatal("GET 不应标记写入")
	}
	uploader := &OSSMultipartUploader{}
	_, _ = uploader.UploadFileWithResult(context.Background(), OSSMultipartUploadInput{AccountID: 1, ParentID: "2", FileSize: -1})
	upload, idle := DirectoryReadVersion(1, "2")
	if !idle || upload == read {
		t.Fatal("上传失败没有使列表失效")
	}
}

func TestDirectoryReadVersionAfterCallerCancellation(t *testing.T) {
	withUnlimitedOpenAPIRequests(t)
	client := NewClient(1, "app", "token", "refresh")
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	setPlaybackTestTransport(t, client, playbackTransportFunc(func(req *http.Request) (*http.Response, error) {
		close(started)
		<-release
		return playbackTestResponse(req, http.StatusOK, `{"state":true,"data":{}}`), nil
	}))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	result := make(chan error, 1)
	go func() { _, err := client.Move(ctx, []string{"1"}, "2"); result <- err }()
	<-started
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	version, idle := DirectoryReadVersion(1, "2")
	if idle {
		t.Fatal("调用方结束后实际写入仍应阻止复用")
	}
	once.Do(func() { close(release) })
	deadline := time.After(time.Second)
	for {
		after, idle := DirectoryReadVersion(1, "2")
		if idle {
			if after == version {
				t.Fatal("实际写入结束没有更新版本")
			}
			break
		}
		select {
		case <-deadline:
			t.Fatal("实际写入未结束")
		case <-time.After(time.Millisecond):
		}
	}
}

func TestDirectoryReadVersionRequestScopes(t *testing.T) {
	withUnlimitedOpenAPIRequests(t)
	for _, tt := range []struct {
		name        string
		endpoint    string
		form        map[string]string
		mutation    bool
		accountWide bool
	}{
		{name: "token refresh POST", endpoint: "/open/refreshToken"},
		{name: "path detail POST", endpoint: "/open/folder/get_info", form: map[string]string{"path": "/media"}},
		{name: "playback URL POST", endpoint: "/open/ufile/downurl", form: map[string]string{"pick_code": "pick"}},
		{name: "copy target", endpoint: "/open/ufile/copy", form: map[string]string{"pid": "2"}, mutation: true},
		{name: "mkdir target", endpoint: "/open/folder/add", form: map[string]string{"pid": "2"}, mutation: true},
		{name: "delete parent", endpoint: "/open/ufile/delete", form: map[string]string{"parent_id": "2"}, mutation: true},
		{name: "rapid upload target", endpoint: "/open/upload/init", form: map[string]string{"target": "U_1_2"}, mutation: true},
		{name: "resume upload target", endpoint: "/open/upload/resume", form: map[string]string{"target": "U_1_2"}, mutation: true},
		{name: "move unknown source", endpoint: "/open/ufile/move", form: map[string]string{"to_cid": "2"}, mutation: true, accountWide: true},
		{name: "rename unknown parent", endpoint: "/open/ufile/update", mutation: true, accountWide: true},
		{name: "delete unknown parent", endpoint: "/open/ufile/delete", mutation: true, accountWide: true},
		{name: "restore unknown parent", endpoint: "/open/rb/revert", mutation: true, accountWide: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			for _, playback := range []bool{false, true} {
				client := NewClient(101, "app", "token", "refresh")
				client.playback = playback
				before, _ := DirectoryReadVersion(101, "2")
				otherDir, _ := DirectoryReadVersion(101, "3")
				otherAccount, _ := DirectoryReadVersion(102, "2")
				setPlaybackTestTransport(t, client, playbackTransportFunc(func(req *http.Request) (*http.Response, error) {
					_, idle := DirectoryReadVersion(101, "2")
					if idle == tt.mutation {
						t.Errorf("playback=%v: target idle=%v", playback, idle)
					}
					if _, idle := DirectoryReadVersion(101, "3"); idle == tt.accountWide {
						t.Errorf("other directory idle=%v", idle)
					}
					if version, idle := DirectoryReadVersion(102, "2"); !idle || version != otherAccount {
						t.Error("other account invalidated")
					}
					return playbackTestResponse(req, http.StatusOK, `{"state":true,"data":{}}`), nil
				}))
				req := client.client.R().SetMethod("POST").SetFormData(tt.form)
				_, _, err := client.doAuthRequest(t.Context(), OPEN_BASE_URL+tt.endpoint, req, MakeRequestConfig(0, 0, 5), nil)
				if err != nil {
					t.Fatal(err)
				}
				after, idle := DirectoryReadVersion(101, "2")
				if !idle || (after != before) != tt.mutation {
					t.Errorf("target version %d -> %d, idle=%v", before, after, idle)
				}
				afterOther, idle := DirectoryReadVersion(101, "3")
				if !idle || (afterOther != otherDir) != tt.accountWide {
					t.Errorf("other directory version %d -> %d, idle=%v", otherDir, afterOther, idle)
				}
			}
		})
	}
}

func TestDirectoryReadVersionMultipartScope(t *testing.T) {
	before, _ := DirectoryReadVersion(103, "2")
	other, _ := DirectoryReadVersion(103, "3")
	account, _ := DirectoryReadVersion(104, "2")
	uploader := &OSSMultipartUploader{}
	_, _ = uploader.UploadFileWithResult(t.Context(), OSSMultipartUploadInput{AccountID: 103, ParentID: "2", FileSize: -1})
	if after, idle := DirectoryReadVersion(103, "2"); after == before || !idle {
		t.Fatal("upload did not invalidate target")
	}
	if after, idle := DirectoryReadVersion(103, "3"); after != other || !idle {
		t.Fatal("upload invalidated other directory")
	}
	if after, idle := DirectoryReadVersion(104, "2"); after != account || !idle {
		t.Fatal("upload invalidated other account")
	}
}
