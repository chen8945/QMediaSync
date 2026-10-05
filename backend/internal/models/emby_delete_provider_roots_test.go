package models

import (
	"errors"
	"io"
	"net/http"
	"testing"

	"qmediasync/internal/baidupan"
	"qmediasync/internal/v115open"
)

func TestEmbyDeleteProviderLocate115ProtectedRoot(t *testing.T) {
	for _, tt := range []struct {
		name   string
		mutate func(*v115open.FileDetail)
		err    error
		wantOK bool
		absent bool
	}{
		{name: "moved root located by original ID", wantOK: true},
		{name: "different ID", mutate: func(d *v115open.FileDetail) { d.FileId = "11" }},
		{name: "not directory", mutate: func(d *v115open.FileDetail) { d.FileCategory = v115open.TypeFile }},
		{name: "missing parent", mutate: func(d *v115open.FileDetail) { d.Paths = nil }},
		{name: "invalid path", mutate: func(d *v115open.FileDetail) { d.Path = "../other" }},
		{name: "original absent", err: v115open.NewOpenAPIError(430004, "gone"), absent: true},
		{name: "failed read", err: errors.New("https://secret.invalid/?token=secret")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			detail := &v115open.FileDetail{FileId: "10", FileName: "nested-root", FileCategory: v115open.TypeDir, Path: "media/Movie", Paths: []v115open.FileDetailPath{{FileId: "20", Name: "Movie"}}}
			if tt.mutate != nil {
				tt.mutate(detail)
			}
			p := &embyDeleteProvider{source: SourceType115, accountID: 1, accountIdentity: "owner", v115: &embyDelete115Stub{detail: detail, err: tt.err}}
			root, err := p.LocateDirectory(t.Context(), "10")
			if (err == nil) != tt.wantOK || errors.Is(err, ErrEmbyRemoteFileAbsent) != tt.absent {
				t.Fatalf("root=%+v error=%v", root, err)
			}
			if tt.wantOK && (root.FileID != "10" || root.ParentID != "20" || root.Path != "/media/Movie" || root.FileName != "nested-root") {
				t.Fatalf("wrong actual location %+v", root)
			}
		})
	}
}

func TestEmbyDeleteProviderLocateBaiduProtectedRoot(t *testing.T) {
	embyProviderTestLogger(t)
	for _, tt := range []struct {
		name, body     string
		wantOK, absent bool
	}{
		{name: "original current location", body: `{"errno":0,"list":[{"fs_id":10,"path":"/media/Movie/nested","filename":"nested","isdir":1}]}`, wantOK: true},
		{name: "replacement ID", body: `{"errno":0,"list":[{"fs_id":11,"path":"/media/Movie/nested","filename":"nested","isdir":1}]}`},
		{name: "missing position", body: `{"errno":0,"list":[{"fs_id":10,"filename":"nested","isdir":1}]}`},
		{name: "original absent", body: `{"errno":0,"list":[]}`, absent: true},
		{name: "failed read", body: `{"errno":500}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			original := http.DefaultClient
			t.Cleanup(func() { http.DefaultClient = original })
			reads := 0
			http.DefaultClient = &http.Client{Transport: baiduUploadTransport(func(w http.ResponseWriter, r *http.Request) {
				reads++
				if r.URL.Query().Get("method") != "filemetas" || r.URL.Query().Get("fsids") != "[10]" {
					t.Errorf("wrong protected-root request %s", r.URL.String())
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, tt.body)
			})}
			p := &embyDeleteProvider{source: SourceTypeBaiduPan, accountID: 1, accountIdentity: "owner", baidu: baidupan.NewBaiDuPanClientWithToken("test")}
			root, err := p.LocateDirectory(t.Context(), "10")
			if (err == nil) != tt.wantOK || errors.Is(err, ErrEmbyRemoteFileAbsent) != tt.absent || reads != 1 {
				t.Fatalf("root=%+v error=%v reads=%d", root, err, reads)
			}
			if tt.wantOK && (root.FileID != "10" || root.Path != "/media/Movie" || root.FileName != "nested") {
				t.Fatalf("wrong actual location %+v", root)
			}
		})
	}
}
