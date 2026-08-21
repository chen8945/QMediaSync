package requests

import (
	"regexp"
	"strings"

	"qmediasync/internal/models"
	"qmediasync/internal/validation"
)

var (
	languageCodePattern = regexp.MustCompile(`^[a-z]{2}(-[A-Z]{2})?$`)
	countryCodePattern  = regexp.MustCompile(`^[A-Z]{2}$`)
)

// TMDBSettingsRequest TMDB 设置请求。
type TMDBSettingsRequest struct {
	TmdbAPIKey        string `json:"tmdb_api_key" form:"tmdb_api_key"`
	TmdbAccessToken   string `json:"tmdb_access_token" form:"tmdb_access_token"`
	TmdbURL           string `json:"tmdb_url" form:"tmdb_url"`
	TmdbImageURL      string `json:"tmdb_image_url" form:"tmdb_image_url"`
	TmdbLanguage      string `json:"tmdb_language" form:"tmdb_language"`
	TmdbImageLanguage string `json:"tmdb_image_language" form:"tmdb_image_language"`
	TmdbEnableProxy   bool   `json:"tmdb_enable_proxy" form:"tmdb_enable_proxy"`
	FanartAPIKey      string `json:"fanart_api_key" form:"fanart_api_key"`
}

// Validate 校验 TMDB 设置请求。
func (r TMDBSettingsRequest) Validate() error {
	if err := validation.HTTPURL("tmdb_url", r.TmdbURL, true); err != nil {
		return err
	}
	if err := validation.HTTPURL("tmdb_image_url", r.TmdbImageURL, true); err != nil {
		return err
	}
	if err := validateLanguageCode("tmdb_language", r.TmdbLanguage, true); err != nil {
		return err
	}
	return validateLanguageCode("tmdb_image_language", r.TmdbImageLanguage, true)
}

// AISettingsRequest AI 设置请求。
type AISettingsRequest struct {
	EnableAI    models.AiAction `json:"enable_ai" form:"enable_ai"`
	AIAPIKey    string          `json:"ai_api_key" form:"ai_api_key"`
	AIBaseURL   string          `json:"ai_base_url" form:"ai_base_url"`
	AIModelName string          `json:"ai_model_name" form:"ai_model_name"`
	AIPrompt    string          `json:"ai_prompt" form:"ai_prompt"`
	AITimeout   int             `json:"ai_timeout" form:"ai_timeout"`
}

// Validate 校验 AI 设置请求。
func (r AISettingsRequest) Validate() error {
	if r.EnableAI != "" {
		if err := validation.OneOfString("enable_ai", string(r.EnableAI), []string{
			string(models.AiActionOff),
			string(models.AiActionAssist),
			string(models.AiActionEnforce),
		}); err != nil {
			return err
		}
	}
	if err := validation.HTTPURL("ai_base_url", r.AIBaseURL, true); err != nil {
		return err
	}
	if strings.TrimSpace(r.AIModelName) != "" {
		if err := validation.Length("ai_model_name", r.AIModelName, 1, 128); err != nil {
			return err
		}
	}
	if r.AITimeout != 0 {
		return validation.RangeInt("ai_timeout", r.AITimeout, 5, 600)
	}
	return nil
}

// validateCategoryName 校验二级分类名可以作为单层目录名使用。
// 分类名会参与目标路径拼接，不能包含路径分隔符和路径穿越片段。
func validateCategoryName(field string, name string) error {
	if strings.ContainsAny(name, `/\`) {
		return validation.New(field, "不能包含路径分隔符")
	}
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return validation.New(field, "不能为空白字符")
	}
	if trimmed == "." || trimmed == ".." {
		return validation.New(field, "不能是 . 或 ..")
	}
	return nil
}

// MovieCategoryRequest 电影分类请求。
type MovieCategoryRequest struct {
	ID            uint     `json:"id" form:"id"`
	Name          string   `json:"name" form:"name"`
	LanguageArray []string `json:"language_array" form:"language_array"`
	GenreIDArray  []int    `json:"genre_id_array" form:"genre_id_array"`
}

// Validate 校验电影分类请求。
func (r MovieCategoryRequest) Validate() error {
	if err := validation.Length("name", r.Name, 1, 64); err != nil {
		return err
	}
	if err := validateCategoryName("name", r.Name); err != nil {
		return err
	}
	for _, language := range r.LanguageArray {
		if err := validateLanguageCode("language_array", language, false); err != nil {
			return err
		}
	}
	return validateGenreIDs(r.GenreIDArray)
}

// TVShowCategoryRequest 电视剧分类请求。
type TVShowCategoryRequest struct {
	ID           uint     `json:"id" form:"id"`
	Name         string   `json:"name" form:"name"`
	CountryArray []string `json:"country_array" form:"country_array"`
	GenreIDArray []int    `json:"genre_id_array" form:"genre_id_array"`
}

// Validate 校验电视剧分类请求。
func (r TVShowCategoryRequest) Validate() error {
	if err := validation.Length("name", r.Name, 1, 64); err != nil {
		return err
	}
	if err := validateCategoryName("name", r.Name); err != nil {
		return err
	}
	for _, country := range r.CountryArray {
		if !countryCodePattern.MatchString(strings.TrimSpace(country)) {
			return validation.New("country_array", "国家代码格式不正确")
		}
	}
	return validateGenreIDs(r.GenreIDArray)
}

// TMDBSearchRequest TMDB 搜索请求。
type TMDBSearchRequest struct {
	Name   string           `json:"name" form:"name"`
	Year   int              `json:"year" form:"year"`
	Type   models.MediaType `json:"type" form:"type" binding:"required"`
	TmdbID int              `json:"tmdb_id" form:"tmdb_id"`
}

// Validate 校验 TMDB 搜索请求。
func (r TMDBSearchRequest) Validate() error {
	if err := validation.OneOfString("type", string(r.Type), []string{
		string(models.MediaTypeMovie),
		string(models.MediaTypeTvShow),
	}); err != nil {
		return err
	}
	if strings.TrimSpace(r.Name) == "" && r.TmdbID == 0 {
		return validation.New("name", "请输入名称或 TMDB ID")
	}
	if r.TmdbID < 0 {
		return validation.New("tmdb_id", "必须大于 0")
	}
	if r.Year != 0 {
		return validation.RangeInt("year", r.Year, 1900, 2100)
	}
	return nil
}

func validateLanguageCode(field string, value string, allowEmpty bool) error {
	value = strings.TrimSpace(value)
	if value == "" {
		if allowEmpty {
			return nil
		}
		return validation.New(field, "不能为空")
	}
	if !languageCodePattern.MatchString(value) {
		return validation.New(field, "语言代码格式不正确")
	}
	return nil
}

func validateGenreIDs(values []int) error {
	for _, value := range values {
		if value <= 0 {
			return validation.New("genre_id_array", "必须大于 0")
		}
	}
	return nil
}
