package requests

import (
	"testing"

	"qmediasync/internal/models"
)

func TestSaveScrapePathRequestValidate(t *testing.T) {
	valid := SaveScrapePathRequest{
		AccountID:          1,
		SourceType:         models.SourceType115,
		MediaType:          models.MediaTypeMovie,
		SourcePath:         "/movies",
		SourcePathID:       "source-id",
		DestPath:           "/library",
		DestPathID:         "dest-id",
		ScrapeType:         models.ScrapeTypeScrapeAndRename,
		RenameType:         models.RenameTypeMove,
		FolderNameTemplate: "{{title}} ({{year}})",
		FileNameTemplate:   "{{title}}",
		VideoExtList:       []string{".mp4", ".mkv"},
		MaxThreads:         5,
		EnableCron:         true,
		CronExpression:     "0 3 * * *",
	}

	tests := []struct {
		name    string
		mutate  func(*SaveScrapePathRequest)
		wantErr bool
	}{
		{name: "合法网盘刮削路径通过"},
		{name: "非本地缺少账号失败", mutate: func(r *SaveScrapePathRequest) { r.AccountID = 0 }, wantErr: true},
		{name: "未知媒体类型失败", mutate: func(r *SaveScrapePathRequest) { r.MediaType = models.MediaType("bad") }, wantErr: true},
		{name: "非本地软链接失败", mutate: func(r *SaveScrapePathRequest) { r.RenameType = models.RenameTypeSoftSymlink }, wantErr: true},
		{name: "非本地线程大于 5 失败", mutate: func(r *SaveScrapePathRequest) { r.MaxThreads = 6 }, wantErr: true},
		{name: "本地线程 20 通过", mutate: func(r *SaveScrapePathRequest) {
			r.SourceType = models.SourceTypeLocal
			r.AccountID = 0
			r.RenameType = models.RenameTypeHardSymlink
			r.MaxThreads = 20
		}},
		{name: "启用 Cron 但表达式为空失败", mutate: func(r *SaveScrapePathRequest) { r.CronExpression = "" }, wantErr: true},
		{name: "模板留空通过", mutate: func(r *SaveScrapePathRequest) {
			r.FolderNameTemplate = ""
			r.FileNameTemplate = ""
		}},
		{name: "模板多级目录通过", mutate: func(r *SaveScrapePathRequest) { r.FolderNameTemplate = "{actors}/{num}" }},
		{name: "文件夹模板含上级目录失败", mutate: func(r *SaveScrapePathRequest) { r.FolderNameTemplate = "../outside" }, wantErr: true},
		{name: "文件夹模板含上级目录片段失败", mutate: func(r *SaveScrapePathRequest) { r.FolderNameTemplate = "{title}/../../outside" }, wantErr: true},
		{name: "文件夹模板为绝对路径失败", mutate: func(r *SaveScrapePathRequest) { r.FolderNameTemplate = "/etc/qms" }, wantErr: true},
		{name: "文件夹模板含反斜杠失败", mutate: func(r *SaveScrapePathRequest) { r.FolderNameTemplate = `..\..\outside` }, wantErr: true},
		{name: "文件模板含上级目录失败", mutate: func(r *SaveScrapePathRequest) { r.FileNameTemplate = "../{title}" }, wantErr: true},
		{name: "文件模板含反斜杠失败", mutate: func(r *SaveScrapePathRequest) { r.FileNameTemplate = `{title}\{num}` }, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := valid
			if tt.mutate != nil {
				tt.mutate(&req)
			}
			err := req.Validate()
			if (err != nil) != tt.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestSaveScrapePathRequestValidateCreate(t *testing.T) {
	valid := SaveScrapePathRequest{
		AccountID:          1,
		SourceType:         models.SourceType115,
		MediaType:          models.MediaTypeMovie,
		SourcePath:         "/movies",
		SourcePathID:       "source-id",
		DestPath:           "/library",
		DestPathID:         "dest-id",
		ScrapeType:         models.ScrapeTypeScrapeAndRename,
		RenameType:         models.RenameTypeMove,
		FolderNameTemplate: "{{title}} ({{year}})",
		FileNameTemplate:   "{{title}}",
		VideoExtList:       []string{".mp4", ".mkv"},
		MaxThreads:         5,
	}

	tests := []struct {
		name    string
		mutate  func(*SaveScrapePathRequest)
		wantErr bool
	}{
		{name: "仅刮削允许目标路径为空", mutate: func(r *SaveScrapePathRequest) {
			r.ScrapeType = models.ScrapeTypeOnly
			r.RenameType = models.RenameTypeSame
			r.DestPath = ""
			r.DestPathID = ""
		}},
		{name: "远程来源允许复制整理", mutate: func(r *SaveScrapePathRequest) {
			r.RenameType = models.RenameTypeCopy
		}},
		{name: "远程来源拒绝软链接整理", mutate: func(r *SaveScrapePathRequest) {
			r.RenameType = models.RenameTypeSoftSymlink
		}, wantErr: true},
		{name: "非仅刮削拒绝 same 整理方式", mutate: func(r *SaveScrapePathRequest) {
			r.RenameType = models.RenameTypeSame
		}, wantErr: true},
		{name: "仅刮削拒绝复制整理方式", mutate: func(r *SaveScrapePathRequest) {
			r.ScrapeType = models.ScrapeTypeOnly
			r.RenameType = models.RenameTypeCopy
			r.DestPath = ""
			r.DestPathID = ""
		}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := valid
			if tt.mutate != nil {
				tt.mutate(&req)
			}
			err := req.ValidateCreate()
			if (err != nil) != tt.wantErr {
				t.Fatalf("ValidateCreate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestSaveScrapePathRequestValidateUpdate(t *testing.T) {
	oldScrapePath := &models.ScrapePath{
		BaseModel:  models.BaseModel{ID: 1},
		AccountId:  1,
		SourceType: models.SourceType115,
		MediaType:  models.MediaTypeMovie,
	}
	valid := SaveScrapePathRequest{
		ID:                 1,
		SourcePath:         "/movies",
		SourcePathID:       "source-id",
		DestPath:           "/library",
		DestPathID:         "dest-id",
		ScrapeType:         models.ScrapeTypeScrapeAndRename,
		RenameType:         models.RenameTypeMove,
		FolderNameTemplate: "{{title}} ({{year}})",
		FileNameTemplate:   "{{title}}",
		VideoExtList:       []string{".mp4", ".mkv"},
		MaxThreads:         5,
	}

	tests := []struct {
		name    string
		req     SaveScrapePathRequest
		old     *models.ScrapePath
		wantErr bool
	}{
		{name: "编辑请求缺少不可编辑字段时使用旧记录校验", req: valid, old: oldScrapePath},
		{name: "编辑请求显式修改来源类型失败", req: func() SaveScrapePathRequest {
			req := valid
			req.SourceType = models.SourceTypeLocal
			return req
		}(), old: oldScrapePath, wantErr: true},
		{name: "编辑仅刮削允许目标路径为空", req: func() SaveScrapePathRequest {
			req := valid
			req.ScrapeType = models.ScrapeTypeOnly
			req.RenameType = models.RenameTypeSame
			req.DestPath = ""
			req.DestPathID = ""
			return req
		}(), old: oldScrapePath},
		{name: "旧记录不存在失败", req: valid, old: nil, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := tt.req
			err := req.ValidateUpdate(tt.old)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ValidateUpdate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
