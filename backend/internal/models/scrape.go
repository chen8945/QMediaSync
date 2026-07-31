package models

import (
	"encoding/json"
	"fmt"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/openai"
	"qmediasync/internal/tmdb"
)

type AiAction string

const (
	AiActionOff     AiAction = "off"     // 关闭 AI 识别
	AiActionAssist  AiAction = "assist"  // 辅助识别，如果预设规则无法识别，则使用 AI 识别结果
	AiActionEnforce AiAction = "enforce" // 强制识别，仅使用 AI 识别结果
)

// 刮削基本设置
type ScrapeSettings struct {
	BaseModel
	TmdbUrl           string   `json:"tmdb_url" form:"tmdb_url"`                       // TMDB API URL，如果不设置则使用默认值
	TmdbImageUrl      string   `json:"tmdb_image_url" form:"tmdb_image_url"`           // TMDB 图片 URL，如果不设置则使用默认值
	TmdbApiKey        string   `json:"api_key" form:"api_key"`                         // TMDB API Key，如果不设置则使用默认值
	TmdbAccessToken   string   `json:"tmdb_access_token" form:"tmdb_access_token"`     // TMDB Access Token，如果不设置则使用默认值
	TmdbLanguage      string   `json:"tmdb_language" form:"tmdb_language"`             // TMDB 语言，默认值为"zh-CN"
	TmdbImageLanguage string   `json:"tmdb_image_language" form:"tmdb_image_language"` // TMDB 图片语言，默认值为"en-US"
	TmdbEnableProxy   bool     `json:"tmdb_enable_proxy" form:"tmdb_enable_proxy"`     // 是否启用 TMDB 代理
	EnableAi          AiAction `json:"enable_ai" form:"enable_ai"`                     // 是否启用 AI 识别
	AiBaseUrl         string   `json:"ai_base_url" form:"ai_base_url"`                 // AI 识别基础 URL
	AiApiKey          string   `json:"ai_api_key" form:"ai_api_key"`                   // AI 识别 API Key
	AiModelName       string   `json:"ai_model_name" form:"ai_model_name"`             // AI 识别模型名称
	AiPrompt          string   `json:"ai_prompt" form:"ai_prompt"`                     // AI 识别提示词，如果留空则使用默认值
	AiTimeout         int      `json:"ai_timeout" form:"ai_timeout"`                   // AI 识别超时时间，单位秒，默认值为 120
	FanartApiKey      string   `json:"fanart_api_key" form:"fanart_api_key"`           // Fanart.tv API Key，如果不设置则使用默认值
}

const (
	DEFAULT_LOCAL_MAX_THREADS = 5
)

var GlobalScrapeSettings = &ScrapeSettings{}

func LoadScrapeSettings() *ScrapeSettings {
	// 检查是否存在记录
	var count int64
	db.Db.Model(&ScrapeSettings{}).Count(&count)

	if count == 0 {
		helpers.AppLogger.Warnf("数据库中没有刮削设置记录，将创建默认记录")
		// 创建默认记录
		InitScrapeSetting()
	}

	if count > 1 {
		helpers.AppLogger.Warnf("数据库中存在多条刮削设置记录（%d 条），将只使用第一条", count)
	}

	if err := db.Db.First(GlobalScrapeSettings).Error; err != nil {
		helpers.AppLogger.Errorf("加载刮削设置失败：%v", err)
		return nil
	}

	GlobalScrapeSettings.ApplyKeyOverrides()
	helpers.AppLogger.Infof("从数据库读取刮削设置成功，ID=%d", GlobalScrapeSettings.ID)
	return GlobalScrapeSettings
}

func (s *ScrapeSettings) GetTmdbApiKey() string {
	if s.TmdbApiKey == "" {
		return helpers.DEFAULT_TMDB_API_KEY
	}
	return s.TmdbApiKey
}

func (s *ScrapeSettings) GetTmdbAccessToken() string {
	if s.TmdbAccessToken == "" {
		return helpers.DEFAULT_TMDB_ACCESS_TOKEN
	}
	return s.TmdbAccessToken
}

func (s *ScrapeSettings) GetFanartApiKey() string {
	if s.FanartApiKey == "" {
		return helpers.DEFAULT_FANART_API_KEY
	}
	return s.FanartApiKey
}

// ApplyKeyOverrides 用 UI 配置覆盖生效密钥/代理（Fanart 客户端直接读取 helpers 包级变量）。
// 取值优先级：UI 配置（数据库） > 默认基线（环境变量 > ldflags）。
func (s *ScrapeSettings) ApplyKeyOverrides() {
	helpers.FANART_API_KEY = s.GetFanartApiKey()
	helpers.HTTP_PROXY = s.GetProxyUrl()
}

// RefreshProxyConsumers 刷新直读生效代理的下游刮削客户端，供代理地址或代理开关变更后调用。
//
// 两类下游都不会自己感知变更：
//   - Fanart 客户端每次构造都读 helpers.HTTP_PROXY，而该变量只由 ApplyKeyOverrides 赋值；
//   - TMDB 是全局单例（tmdb.GlobalTmdbClient），不重新走构造函数就不会更新 resty 的代理设置。
//
// 不刷新会导致：切换代理后 Fanart 继续拨已下线的旧代理；清空代理或关闭代理开关后
// TMDB 继续把 API Key 从用户已撤销的代理隧道里送出去。
func RefreshProxyConsumers() {
	if GlobalScrapeSettings == nil {
		return
	}
	GlobalScrapeSettings.ApplyKeyOverrides()
	if tmdb.GlobalTmdbClient != nil {
		tmdb.GlobalTmdbClient.SetProxyUrl(GlobalScrapeSettings.GetProxyUrl())
	}
}

func (s *ScrapeSettings) GetTmdbLanguage() string {
	if s.TmdbLanguage == "" {
		return helpers.DEFAULT_TMDB_LANGUAGE
	}
	return s.TmdbLanguage
}

func (s *ScrapeSettings) GetTmdbImageLanguage() string {
	if s.TmdbImageLanguage == "" {
		return helpers.DEFAULT_TMDB_IMAGE_LANGUAGE
	}
	return s.TmdbImageLanguage
}

func (s *ScrapeSettings) GetTmdbApiUrl() string {
	if s.TmdbUrl == "" {
		return helpers.DEFAULT_TMDB_API_URL
	}
	return s.TmdbUrl
}

func (s *ScrapeSettings) GetTmdbImageUrl() string {
	if s.TmdbImageUrl == "" {
		return helpers.DEFAULT_TMDB_IMAGE_URL
	}
	return s.TmdbImageUrl
}

// GetProxyUrl 返回生效的通用代理地址：代理开关开启时用全局 HttpProxy，否则返回空（直连）。TMDB、Fanart 等刮削客户端共用。
func (s *ScrapeSettings) GetProxyUrl() string {
	if s.TmdbEnableProxy {
		return SettingsGlobal.HttpProxy
	}
	return ""
}

func (s *ScrapeSettings) GetTmdbClient() *tmdb.Client {
	return tmdb.NewClient(s.GetTmdbApiKey(), s.GetTmdbAccessToken(), s.GetTmdbApiUrl(), s.GetTmdbLanguage(), s.GetProxyUrl())
}

// 保存 TMDB 设置
func (s *ScrapeSettings) SaveTmdb(apiKey, accessToken string, apiUrl string, imageUrl string, language string, imageLanguage string, enableProxy bool) error {
	// 更新全局对象
	s.TmdbApiKey = apiKey
	s.TmdbAccessToken = accessToken
	s.TmdbUrl = apiUrl
	s.TmdbImageUrl = imageUrl
	s.TmdbLanguage = language
	s.TmdbImageLanguage = imageLanguage
	s.TmdbEnableProxy = enableProxy

	// 准备更新数据
	updateData := make(map[string]interface{})
	updateData["tmdb_api_key"] = apiKey
	updateData["tmdb_access_token"] = accessToken
	updateData["tmdb_url"] = apiUrl
	updateData["tmdb_image_url"] = imageUrl
	updateData["tmdb_language"] = language
	updateData["tmdb_image_language"] = imageLanguage
	updateData["tmdb_enable_proxy"] = enableProxy

	// 使用 s 作为 Model 参数，确保更新正确的记录
	result := db.Db.Model(s).Updates(updateData)
	if result.Error != nil {
		helpers.AppLogger.Errorf("更新 TMDB 设置失败：%v", result.Error)
		return result.Error
	}

	// 检查是否真的更新了记录
	if result.RowsAffected == 0 {
		helpers.AppLogger.Warnf("更新 TMDB 设置：没有记录被更新，ID=%d", s.ID)
		return fmt.Errorf("没有找到要更新的记录")
	}

	helpers.AppLogger.Infof("TMDB 设置已成功更新，ID=%d，影响行数=%d", s.ID, result.RowsAffected)
	// 代理开关是 GetProxyUrl 的入参，改完必须刷新下游：否则关闭开关后 TMDB 单例仍走旧代理
	RefreshProxyConsumers()
	return nil
}

// SaveKeys 保存 Fanart 密钥，并立即刷新生效值。
func (s *ScrapeSettings) SaveKeys(fanartApiKey string) error {
	s.FanartApiKey = fanartApiKey
	updateData := map[string]interface{}{
		"fanart_api_key": fanartApiKey,
	}
	if err := db.Db.Model(s).Where("id = ?", s.ID).Updates(updateData).Error; err != nil {
		helpers.AppLogger.Errorf("更新密钥设置失败：%v", err)
		return err
	}
	s.ApplyKeyOverrides()
	return nil
}

// 获取 AI 识别提示词
func (s *ScrapeSettings) GetAiPrompt() string {
	var prompt string = s.AiPrompt
	if prompt == "" {
		prompt = openai.DEFAULT_MOVIE_PROMPT
	}
	return prompt
}

func (s *ScrapeSettings) GetAiApiKey() string {
	if s.EnableAi == AiActionAssist && s.AiApiKey == "" {
		return helpers.DEFAULT_SC_API_KEY
	}
	return s.AiApiKey
}

func (s *ScrapeSettings) GetAiBaseUrl() string {
	if s.EnableAi == AiActionAssist && s.AiBaseUrl == "" {
		return openai.DEFAULT_API_BASE_URL
	}
	return s.AiBaseUrl
}

func (s *ScrapeSettings) GetAiTimeout() int {
	if s.AiTimeout == 0 {
		return openai.DEFAULT_TIMEOUT
	}
	return s.AiTimeout
}

func (s *ScrapeSettings) GetAiModelName() string {
	if s.EnableAi == AiActionAssist && s.AiModelName == "" {
		return openai.DEFAULT_MODEL_NAME
	}
	return s.AiModelName
}

func (s *ScrapeSettings) GetAiClient() *openai.Client {
	return openai.NewClient(s.GetAiApiKey(), s.GetAiBaseUrl(), s.GetAiModelName(), s.GetAiTimeout())
}

// 保存 AI 识别设置
func (s *ScrapeSettings) SaveAi(apiKey string, baseUrl string, modelName string, timeout int) error {
	s.AiApiKey = apiKey
	s.AiBaseUrl = baseUrl
	s.AiModelName = modelName
	s.AiTimeout = timeout
	updateData := make(map[string]interface{})
	updateData["ai_api_key"] = apiKey
	updateData["ai_base_url"] = baseUrl
	updateData["ai_model_name"] = modelName
	updateData["ai_timeout"] = s.AiTimeout
	// helpers.AppLogger.Infof("更新 AI 识别设置：%+v", updateData)
	err := db.Db.Model(s).Where("id = ?", s.ID).Updates(updateData).Error
	if err != nil {
		helpers.AppLogger.Errorf("更新 AI 识别设置失败：%v", err)
		return err
	}
	return nil
}

// 测试 TMDB 是否配置正确
func (s *ScrapeSettings) TestTmdb() bool {
	client := s.GetTmdbClient()
	return client.TestToken()
}

// 测试 AI 是否配置正确
func (s *ScrapeSettings) TestAi() error {
	if s.EnableAi == AiActionOff {
		return nil
	}
	prompt := s.GetAiPrompt()
	filename := "[银色子弹字幕组][名侦探柯南][第74集 死神阵内杀人事件][WEBRIP][简日双语MP4/繁日双语MP4/简繁日多语MKV][1080P]"
	client := s.GetAiClient()
	mediaInfo, err := client.TakeMoiveName(filename, prompt)
	if err != nil {
		helpers.AppLogger.Errorf("测试 AI 识别失败：%v", err)
		return err
	}
	helpers.AppLogger.Infof("测试 AI 识别成功：文件名：%s，AI 识别结果：%+v", filename, mediaInfo)
	if mediaInfo.Name == "名侦探柯南" {
		return nil
	}
	return fmt.Errorf("测试 AI 识别失败，识别出的电影名称不是名侦探柯南")
}

func (s *ScrapeSettings) ExtractByAi(filename string) (*openai.MediaInfoAI, error) {
	client := s.GetAiClient()
	return client.TakeMoiveName(filename, s.GetAiPrompt())
}

// 电影分类
// 默认 ID=1，外语剧分类，语言为空，无法修改
// 语言为空则不匹配 TMDB 的 original_language
// 分类 ID 为空则不匹配 TMDB 的 genre_ids
// TMDB 的 genre_ids 是一个数组，只要数组中有一个分类 ID 匹配，就认为是该分类
// 新增和修改分类时给所有电影类型的刮削目录创建分类文件夹（如果不存在）
type MovieCategory struct {
	BaseModel
	Name          string   `json:"name"`                    // 分类名称
	GenreIds      string   `json:"-"`                       // 分类 ID，JSON 数字数组
	Language      string   `json:"-"`                       // 语言，JSON 字符串数组，如果为空则排除所有已设置的语言
	GenreIdArray  []int    `json:"genre_id_array" gorm:"-"` // 分类 ID 数组
	LanguageArray []string `json:"language_array" gorm:"-"` // 语言数组
}

// 剧集分类
// 默认 ID=1，外语剧分类，语言为空，无法修改
// 语言为空则不匹配 TMDB 的 original_language
// 分类 ID 为空则不匹配 TMDB 的 genre_ids
// TMDB 的 genre_ids 是一个数组，只要数组中有一个分类 ID 匹配，就认为是该分类
// 新增和修改分类时给所有剧集类型的刮削目录创建分类文件夹（如果不存在）
type TvShowCategory struct {
	BaseModel
	Name         string   `json:"name"`                    // 分类名称
	GenreIds     string   `json:"-"`                       // 分类 ID，JSON 数字数组
	Countries    string   `json:"-"`                       // 国家，JSON 字符串数组，如果为空则排除所有已设置的国家
	GenreIdArray []int    `json:"genre_id_array" gorm:"-"` // 分类 ID 数组
	CountryArray []string `json:"country_array" gorm:"-"`  // 国家数组
}

// 刮削目录分类
// 一个刮削目录可以对应多个分类，每个分类有一个文件 ID，用于指定分类下的文件存储路径
type ScrapePathCategory struct {
	BaseModel
	ScrapePathId uint   `json:"scrape_path_id"` // 刮削目录 ID
	CategoryId   uint   `json:"category_id"`    // 分类 ID
	FileId       string `json:"file_id"`        // 文件 ID，115 文件 ID、123 文件夹 ID；local 和 OpenList 则为相对路径
}

// 查询电影分类列表，不分页
func GetMovieCategory() []*MovieCategory {
	var movieGenre []*MovieCategory
	db.Db.Model(&MovieCategory{}).Find(&movieGenre)
	// 将 GenreIds 和 Language 转换为数组
	for _, genre := range movieGenre {
		genre.GenreIdArray = []int{}
		genre.LanguageArray = []string{}

		if genre.GenreIds != "" {
			err := json.Unmarshal([]byte(genre.GenreIds), &genre.GenreIdArray)
			if err != nil {
				helpers.AppLogger.Errorf("转换电影分类 ID 失败：%v", err)
			}
		}
		if genre.Language != "" {
			err := json.Unmarshal([]byte(genre.Language), &genre.LanguageArray)
			if err != nil {
				helpers.AppLogger.Errorf("转换语言失败：%v", err)
			}
		}
	}
	return movieGenre
}

func GetTvshowCategory() []*TvShowCategory {
	var tvshowGenre []*TvShowCategory
	db.Db.Model(&TvShowCategory{}).Find(&tvshowGenre)
	// 将 GenreIds 和 Language 转换为数组
	for _, genre := range tvshowGenre {
		genre.GenreIdArray = []int{}
		genre.CountryArray = []string{}

		if genre.GenreIds != "" {
			err := json.Unmarshal([]byte(genre.GenreIds), &genre.GenreIdArray)
			if err != nil {
				helpers.AppLogger.Errorf("转换剧集分类 ID 失败：%v", err)
			}
		}
		if genre.Countries != "" {
			err := json.Unmarshal([]byte(genre.Countries), &genre.CountryArray)
			if err != nil {
				helpers.AppLogger.Errorf("转换国家失败：%v", err)
			}
		}
	}
	return tvshowGenre
}

// 保存或者更新电影分类
func (m *MovieCategory) Save(name string, genreIdsArray []int, languageArray []string) error {
	m.Name = name
	m.GenreIdArray = genreIdsArray
	m.LanguageArray = languageArray
	if len(genreIdsArray) == 0 {
		m.GenreIds = ""
	} else {
		genreIds, err := json.Marshal(genreIdsArray)
		if err != nil {
			helpers.AppLogger.Errorf("转换电影分类 ID 失败：%v", err)
			return err
		}
		m.GenreIds = string(genreIds)
	}
	if len(languageArray) == 0 {
		m.Language = ""
	} else {
		language, err := json.Marshal(languageArray)
		if err != nil {
			helpers.AppLogger.Errorf("转换语言失败：%v", err)
			return err
		}
		m.Language = string(language)
	}
	if m.ID == 0 {
		db.Db.Save(m)
	} else {
		db.Db.Save(m)
	}
	return nil
}

// 保存或者更新剧集分类
func (m *TvShowCategory) Save(name string, genreIdsArray []int, countryArray []string) error {
	m.Name = name
	m.GenreIdArray = genreIdsArray
	m.CountryArray = countryArray
	if len(genreIdsArray) == 0 {
		m.GenreIds = ""
	} else {
		genreIds, err := json.Marshal(genreIdsArray)
		if err != nil {
			helpers.AppLogger.Errorf("转换剧集分类 ID 失败：%v", err)
			return err
		}
		m.GenreIds = string(genreIds)
	}
	if len(countryArray) == 0 {
		m.Countries = ""
	} else {
		countries, err := json.Marshal(countryArray)
		if err != nil {
			helpers.AppLogger.Errorf("转换国家失败：%v", err)
			return err
		}
		m.Countries = string(countries)
	}
	if m.ID == 0 {
		db.Db.Save(m)
	} else {
		db.Db.Save(m)
	}
	return nil
}

// 删除电影分类
func DeleteMovieCategory(id uint) error {
	delErr := db.Db.Delete(&MovieCategory{}, id).Error
	if delErr != nil {
		helpers.AppLogger.Errorf("删除电影分类失败：%v", delErr)
		return delErr
	}
	// 尝试删除关联的 ScrapePathCategory
	if delErr = db.Db.Delete(&ScrapePathCategory{}, "category_id = ?", id).Error; delErr != nil {
		helpers.AppLogger.Errorf("删除关联的 ScrapePathCategory 失败：%v", delErr)
		return delErr
	}
	return nil
}

// 删除电视剧分类
func DeleteTvshowCategory(id uint) error {
	delErr := db.Db.Delete(&TvShowCategory{}, id).Error
	if delErr != nil {
		helpers.AppLogger.Errorf("删除剧集分类失败：%v", delErr)
		return delErr
	}
	// 尝试删除关联的 ScrapePathCategory
	if delErr = db.Db.Delete(&ScrapePathCategory{}, "category_id = ?", id).Error; delErr != nil {
		helpers.AppLogger.Errorf("删除关联的 ScrapePathCategory 失败：%v", delErr)
		return delErr
	}
	return nil
}

// 查询所有刮削目录分类
func GetAllScrapePathCategory(scrapePathId uint) []*ScrapePathCategory {
	var scrapePathCategory []*ScrapePathCategory
	db.Db.Model(&ScrapePathCategory{}).Where("scrape_path_id = ?", scrapePathId).Find(&scrapePathCategory)
	return scrapePathCategory
}

func SaveScrapePathCategory(spcId, scrapePathId uint, categoryId uint, fileId string) (*ScrapePathCategory, error) {
	var scrapePathCategory *ScrapePathCategory
	var err error
	if spcId > 0 {
		scrapePathCategory = GetScrapePathCategoryById(spcId)
		scrapePathCategory.FileId = fileId
		err = db.Db.Save(scrapePathCategory).Error
	} else {
		scrapePathCategory = &ScrapePathCategory{
			ScrapePathId: scrapePathId,
			CategoryId:   categoryId,
			FileId:       fileId,
		}
		// 保存新记录
		err = db.Db.Save(scrapePathCategory).Error
	}
	if err != nil {
		helpers.AppLogger.Errorf("保存刮削目录分类失败：%v", err)
		return nil, err
	}
	return scrapePathCategory, nil
}

func (s *ScrapePathCategory) Delete() error {
	return db.Db.Delete(s).Error
}
