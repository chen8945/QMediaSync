package requests

import (
	"encoding/json"
	"testing"

	"qmediasync/internal/models"
)

func TestFnosProxyRequestValidation(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*SaveFnosProxyConfigRequest)
		bad  bool
	}{
		{name: "valid"},
		{name: "host shorthand", edit: func(r *SaveFnosProxyConfigRequest) { r.FnosURL = "http://:8005" }},
		{name: "trimmed root", edit: func(r *SaveFnosProxyConfigRequest) { r.FnosURL = " http://fnos.invalid/ " }},
		{name: "empty enabled url", edit: func(r *SaveFnosProxyConfigRequest) { r.FnosURL = "" }, bad: true},
		{name: "userinfo", edit: func(r *SaveFnosProxyConfigRequest) { r.FnosURL = "http://user:pass@fnos.invalid" }, bad: true},
		{name: "path", edit: func(r *SaveFnosProxyConfigRequest) { r.FnosURL += "/v/api" }, bad: true},
		{name: "query", edit: func(r *SaveFnosProxyConfigRequest) { r.FnosURL += "?token=a" }, bad: true},
		{name: "empty query", edit: func(r *SaveFnosProxyConfigRequest) { r.FnosURL += "?" }, bad: true},
		{name: "fragment", edit: func(r *SaveFnosProxyConfigRequest) { r.FnosURL += "#a" }, bad: true},
		{name: "empty fragment", edit: func(r *SaveFnosProxyConfigRequest) { r.FnosURL += "#" }, bad: true},
		{name: "encoded path", edit: func(r *SaveFnosProxyConfigRequest) { r.FnosURL += "/%2F" }, bad: true},
		{name: "scheme", edit: func(r *SaveFnosProxyConfigRequest) { r.FnosURL = "file:///media" }, bad: true},
		{name: "relative mapping", edit: func(r *SaveFnosProxyConfigRequest) {
			r.PathMappings = []models.FnosPathMapping{{FnosRoot: "media", LocalRoot: "/media"}}
		}, bad: true},
		{name: "overlap", edit: func(r *SaveFnosProxyConfigRequest) {
			r.PathMappings = []models.FnosPathMapping{{FnosRoot: "/media", LocalRoot: "/a"}, {FnosRoot: "/media/films", LocalRoot: "/b"}}
		}, bad: true},
		{name: "too many mappings", edit: func(r *SaveFnosProxyConfigRequest) { r.PathMappings = make([]models.FnosPathMapping, 65) }, bad: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := SaveFnosProxyConfigRequest{Enabled: true, FnosURL: "http://fnos.invalid:8005"}
			if test.edit != nil {
				test.edit(&request)
			}
			if err := request.Validate(); (err != nil) != test.bad {
				t.Fatalf("validation: %v", err)
			}
		})
	}
	if err := (SaveFnosProxyConfigRequest{}).Validate(); err != nil {
		t.Fatalf("disabled defaults must save: %v", err)
	}
	for _, body := range []string{`{"enabled":1}`, `{"path_mappings":{}}`} {
		var request SaveFnosProxyConfigRequest
		if err := json.Unmarshal([]byte(body), &request); err == nil {
			t.Fatalf("invalid input type accepted: %s", body)
		}
	}
	config, err := (SaveFnosProxyConfigRequest{FnosURL: " http://fnos.invalid/ "}).Config()
	if err != nil || config.FnosURL != "http://fnos.invalid" || config.PathMappings == nil {
		t.Fatalf("canonical config: %+v %v", config, err)
	}
}
