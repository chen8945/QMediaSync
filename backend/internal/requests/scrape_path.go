package requests

import (
	"strings"

	"qmediasync/internal/models"
	"qmediasync/internal/validation"
)

// SaveScrapePathRequest 保存刮削路径请求。
type SaveScrapePathRequest struct {
	ID                    uint              `json:"id" form:"id"`
	AccountID             uint              `json:"account_id" form:"account_id"`
	SourceType            models.SourceType `json:"source_type" form:"source_type"`
	MediaType             models.MediaType  `json:"media_type" form:"media_type"`
	SourcePath            string            `json:"source_path" form:"source_path"`
	SourcePathID          string            `json:"source_path_id" form:"source_path_id"`
	DestPath              string            `json:"dest_path" form:"dest_path"`
	DestPathID            string            `json:"dest_path_id" form:"dest_path_id"`
	ScrapeType            models.ScrapeType `json:"scrape_type" form:"scrape_type"`
	RenameType            models.RenameType `json:"rename_type" form:"rename_type"`
	FolderNameTemplate    string            `json:"folder_name_template" form:"folder_name_template"`
	FileNameTemplate      string            `json:"file_name_template" form:"file_name_template"`
	DeleteKeyword         []string          `json:"delete_keyword" form:"delete_keyword"`
	EnableCategory        bool              `json:"enable_category" form:"enable_category"`
	VideoExtList          []string          `json:"video_ext_list" form:"video_ext_list"`
	MinVideoFileSize      int64             `json:"min_video_file_size" form:"min_video_file_size"`
	ExcludeNoImageActor   bool              `json:"exclude_no_image_actor" form:"exclude_no_image_actor"`
	EnableAi              models.AiAction   `json:"enable_ai" form:"enable_ai"`
	AiPrompt              string            `json:"ai_prompt" form:"ai_prompt"`
	ForceDeleteSourcePath bool              `json:"force_delete_source_path" form:"force_delete_source_path"`
	EnableCron            bool              `json:"enable_cron" form:"enable_cron"`
	CronExpression        string            `json:"cron_expression" form:"cron_expression"`
	EnableFanartTv        bool              `json:"enable_fanart_tv" form:"enable_fanart_tv"`
	MaxThreads            int               `json:"max_threads" form:"max_threads"`
}

// Validate 校验刮削路径创建请求。
func (r SaveScrapePathRequest) Validate() error {
	return r.ValidateCreate()
}

// ValidateCreate 校验新增刮削路径请求。
func (r SaveScrapePathRequest) ValidateCreate() error {
	return r.validate()
}

// ValidateUpdate 使用旧记录补齐不可编辑字段后校验更新请求。
func (r *SaveScrapePathRequest) ValidateUpdate(oldScrapePath *models.ScrapePath) error {
	if err := validation.PositiveID("id", r.ID); err != nil {
		return err
	}
	if oldScrapePath == nil {
		return validation.New("id", "刮削路径不存在")
	}
	if r.SourceType != "" && r.SourceType != oldScrapePath.SourceType {
		return validation.New("source_type", "不能修改来源类型")
	}
	if r.AccountID != 0 && r.AccountID != oldScrapePath.AccountId {
		return validation.New("account_id", "不能修改账号")
	}
	if r.MediaType != "" && r.MediaType != oldScrapePath.MediaType {
		return validation.New("media_type", "不能修改媒体类型")
	}
	r.SourceType = oldScrapePath.SourceType
	r.AccountID = oldScrapePath.AccountId
	r.MediaType = oldScrapePath.MediaType
	return r.validate()
}

func (r SaveScrapePathRequest) validate() error {
	sourceValues := []string{
		string(models.SourceTypeLocal),
		string(models.SourceType115),
		string(models.SourceType123),
		string(models.SourceTypeOpenList),
		string(models.SourceTypeBaiduPan),
	}
	if err := validation.OneOfString("source_type", string(r.SourceType), sourceValues); err != nil {
		return err
	}
	if r.SourceType != models.SourceTypeLocal && r.AccountID == 0 {
		return validation.New("account_id", "非本地来源必须选择账号")
	}
	if err := validation.OneOfString("media_type", string(r.MediaType), []string{
		string(models.MediaTypeMovie),
		string(models.MediaTypeTvShow),
		string(models.MediaTypeOther),
	}); err != nil {
		return err
	}
	if err := validation.OneOfString("scrape_type", string(r.ScrapeType), []string{
		string(models.ScrapeTypeOnly),
		string(models.ScrapeTypeScrapeAndRename),
		string(models.ScrapeTypeOnlyRename),
	}); err != nil {
		return err
	}
	if err := validateScrapePathRenameType(r.SourceType, r.ScrapeType, r.RenameType); err != nil {
		return err
	}
	if err := validateNameTemplate("folder_name_template", r.FolderNameTemplate); err != nil {
		return err
	}
	if err := validateNameTemplate("file_name_template", r.FileNameTemplate); err != nil {
		return err
	}
	if err := validation.NonBlank("source_path", r.SourcePath); err != nil {
		return err
	}
	if r.ScrapeType != models.ScrapeTypeOnly {
		if err := validation.NonBlank("dest_path", r.DestPath); err != nil {
			return err
		}
	}
	if err := validation.ExtList("video_ext_list", r.VideoExtList, true); err != nil {
		return err
	}
	if r.MinVideoFileSize < 0 {
		return validation.New("min_video_file_size", "不能小于 0")
	}
	maxThreads := 5
	if r.SourceType == models.SourceTypeLocal {
		maxThreads = 20
	}
	if err := validation.RangeInt("max_threads", r.MaxThreads, 1, maxThreads); err != nil {
		return err
	}
	if r.EnableCron {
		return validation.Cron("cron_expression", r.CronExpression, false)
	}
	return validation.Cron("cron_expression", r.CronExpression, true)
}

// validateNameTemplate 校验命名模板不会跨出目标根目录。
// 模板可以用 / 分隔多级目录，但不能是绝对路径，也不能包含反斜杠和 .. 片段。
func validateNameTemplate(field string, template string) error {
	if template == "" {
		return nil
	}
	if strings.Contains(template, `\`) {
		return validation.New(field, "不能包含反斜杠，多级目录请使用 /")
	}
	if strings.HasPrefix(template, "/") {
		return validation.New(field, "不能以 / 开头")
	}
	for _, segment := range strings.Split(template, "/") {
		if strings.TrimSpace(segment) == ".." {
			return validation.New(field, "不能包含 .. 路径片段")
		}
	}
	return nil
}

func validateScrapePathRenameType(sourceType models.SourceType, scrapeType models.ScrapeType, renameType models.RenameType) error {
	if scrapeType == models.ScrapeTypeOnly {
		if renameType != models.RenameTypeSame {
			return validation.New("rename_type", "仅刮削不需要整理方式")
		}
		return nil
	}

	allowed := []string{
		string(models.RenameTypeMove),
	}
	switch sourceType {
	case models.SourceTypeLocal:
		allowed = append(allowed,
			string(models.RenameTypeCopy),
			string(models.RenameTypeSoftSymlink),
			string(models.RenameTypeHardSymlink),
		)
	case models.SourceType115, models.SourceTypeBaiduPan, models.SourceTypeOpenList:
		allowed = append(allowed, string(models.RenameTypeCopy))
	}
	if err := validation.OneOfString("rename_type", string(renameType), allowed); err != nil {
		return err
	}
	return nil
}

// ToModel 转换为刮削路径模型。
func (r SaveScrapePathRequest) ToModel() models.ScrapePath {
	return models.ScrapePath{
		BaseModel:             models.BaseModel{ID: r.ID},
		AccountId:             r.AccountID,
		SourceType:            r.SourceType,
		MediaType:             r.MediaType,
		SourcePath:            r.SourcePath,
		SourcePathId:          r.SourcePathID,
		DestPath:              r.DestPath,
		DestPathId:            r.DestPathID,
		ScrapeType:            r.ScrapeType,
		RenameType:            r.RenameType,
		FolderNameTemplate:    r.FolderNameTemplate,
		FileNameTemplate:      r.FileNameTemplate,
		DeleteKeyword:         r.DeleteKeyword,
		EnableCategory:        r.EnableCategory,
		VideoExtList:          r.VideoExtList,
		MinVideoFileSize:      r.MinVideoFileSize,
		ExcludeNoImageActor:   r.ExcludeNoImageActor,
		EnableAi:              r.EnableAi,
		AiPrompt:              r.AiPrompt,
		ForceDeleteSourcePath: r.ForceDeleteSourcePath,
		EnableCron:            r.EnableCron,
		CronExpression:        r.CronExpression,
		EnableFanartTv:        r.EnableFanartTv,
		MaxThreads:            r.MaxThreads,
	}
}
