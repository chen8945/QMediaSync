package models

import (
	"encoding/json"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
)

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
	FileId       string `json:"file_id"`        // 文件 ID，local 和 OpenList 使用相对路径
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
	return db.Db.Save(m).Error
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
	return db.Db.Save(m).Error
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
