package tmdb

import (
	"context"
	"fmt"

	"qmediasync/internal/helpers"
)

type SearchMovie struct {
	Adult            bool    `json:"adult"`             // 是否成人内容
	BackdropPath     string  `json:"backdrop_path"`     // 背景图片（选定语言的版本）
	GenreIDs         []int   `json:"genre_ids"`         // 流派 ID
	ID               int64   `json:"id"`                // ID
	Title            string  `json:"title"`             // 标题（选定语言的版本）
	OriginalTitle    string  `json:"original_title"`    // 原始标题
	Overview         string  `json:"overview"`          // 描述
	PosterPath       string  `json:"poster_path"`       // 封面图片（选定语言的版本）
	ReleaseDate      string  `json:"release_date"`      // 上映日期
	Popularity       float64 `json:"popularity"`        // 流行度
	VoteAverage      float64 `json:"vote_average"`      // 平均评分
	VoteCount        int64   `json:"vote_count"`        // 投票数
	OriginalLanguage string  `json:"original_language"` // 原始语言
}

type SearchMovieResponse struct {
	Page         int           `json:"page"`
	TotalResults int           `json:"total_results"`
	TotalPages   int           `json:"total_pages"`
	Results      []SearchMovie `json:"results"`
}

type Genre struct {
	ID   int    `json:"id"`   // 流派 ID
	Name string `json:"name"` // 流派名称
}

type ProductionCompany struct {
	ID   int    `json:"id"`   // 公司 ID
	Name string `json:"name"` // 公司名称
}

type Country struct {
	ISO_3166_1 string `json:"iso_3166_1"` // 国家代码
	Name       string `json:"name"`       // 国家名称
}

type Language struct {
	EnglishName string `json:"english_name"` // 英文名称
	ISO_639_1   string `json:"iso_639_1"`    // 语言代码
	Name        string `json:"name"`         // 名称
}

// 电影详情
type MovieDetail struct {
	SearchMovie
	Genres              []Genre             `json:"genres"`               // 流派
	ProductionCompanies []ProductionCompany `json:"production_companies"` // 生产公司
	ProductionCountries []Country           `json:"production_countries"` // 生产国家
	Revenue             int64               `json:"revenue"`              // 票房
	Runtime             int64               `json:"runtime"`              // 运行时间
	SpokenLanguages     []Language          `json:"spoken_languages"`     // 语言
	Status              string              `json:"status"`               // 状态
	Tagline             string              `json:"tagline"`              // 标语
	Homepage            string              `json:"homepage"`             // 首页
	ImdbID              string              `json:"imdb_id"`              // IMDb ID
}

type PeopleBase struct {
	ID                 int64   `json:"id"`                   // 演员 ID
	Name               string  `json:"name"`                 // 演员名称
	OriginalName       string  `json:"original_name"`        // 演员原始名称
	Adult              bool    `json:"adult"`                // 是否成人内容
	Gender             int     `json:"gender"`               // 性别
	ProfilePath        string  `json:"profile_path"`         // 头像路径（选定语言）
	Order              int64   `json:"order"`                // 顺序
	Popularity         float64 `json:"popularity"`           // 流行度
	CastID             int64   `json:"cast_id"`              // 演员 ID（在影片中的 ID）
	CreditID           string  `json:"credit_id"`            // 演员在影片中的 ID
	KnownForDepartment string  `json:"known_for_department"` // 演员在影片中的部门
}

type Cast struct {
	PeopleBase
	Character string `json:"character"` // 角色名称
}

type Crew struct {
	PeopleBase
	Department string `json:"department"` // 部门
	Job        string `json:"job"`        // 职务
}

type PepolesRes struct {
	ID   int64  `json:"id"`   // 影片 ID
	Cast []Cast `json:"cast"` // 演员列表
	Crew []Crew `json:"crew"` // 制作人员列表
}
type Image struct {
	AspectRatio float64 `json:"aspect_ratio"` // 宽高比
	FilePath    string  `json:"file_path"`    // 图片路径
	Height      int     `json:"height"`       // 高度
	Width       int     `json:"width"`        // 宽度
	ISO_639_1   string  `json:"iso_639_1"`    // 语言代码
}
type Images struct {
	ID        int64   `json:"id"`        // 影片 ID
	Backdrops []Image `json:"backdrops"` // 背景图片列表
	Posters   []Image `json:"posters"`   // 封面图片列表
	Logos     []Image `json:"logos"`     // logo 图片列表
}

type Keyword struct {
	ID   int64  `json:"id"`   // 关键词 ID
	Name string `json:"name"` // 关键词名称
}
type MovieKeywords struct {
	ID       int64     `json:"id"`       // 影片 ID
	Keywords []Keyword `json:"keywords"` // 关键词列表
}

type ReleaseDate struct {
	Certification string `json:"certification"` // 认证等级
	ISO_639_1     string `json:"iso_639_1"`     // 语言代码
	Note          string `json:"note"`          // 备注
	ReleaseDate   string `json:"release_date"`  // 发布日期
	Type          int64  `json:"type"`          // 类型
}

type ReleasesDateResult struct {
	ISO_3166_1   string        `json:"iso_3166_1"`    // 国家代码
	ReleaseDates []ReleaseDate `json:"release_dates"` // 发布日期列表
}

type ReleasesDateResp struct {
	ID      int64                `json:"id"`      // 影片 ID
	Results []ReleasesDateResult `json:"results"` // 发布日期列表
}

// https://developers.themoviedb.org/3/search/search-movies
func (c *Client) SearchMovie(ctx context.Context, movieName string, year int, language string, switchLanguage bool, switchYear bool) (*SearchMovieResponse, error) {
	respResult := SearchMovieResponse{}
	req := c.resty.R().SetContext(ctx).SetMethod("GET").SetResult(&respResult)
	req.SetQueryParam("query", movieName)
	if year > 0 {
		if switchYear {
			req.SetQueryParam("year", fmt.Sprintf("%d", year))
		} else {
			req.SetQueryParam("primary_release_year", fmt.Sprintf("%d", year))
		}
	}
	if language != "" {
		req.SetQueryParam("language", language)
	}
	resp, err := c.doRequest("/search/movie", req, MakeRequestConfig(2, 5, 5))
	if err != nil {
		helpers.TMDBLog.Errorf("搜索电影失败：%+v", err)
		return nil, err
	}
	if !resp.IsStatusSuccess() {
		helpers.TMDBLog.Errorf("搜索电影失败：%s", resp.String())
		return nil, fmt.Errorf("搜索电影失败：%s", resp.String())
	}
	if len(respResult.Results) == 0 && switchLanguage {
		// 换一种语言搜索
		if language == "en-US" {
			language = "zh-CN"
		} else {
			language = "en-US"
		}
		return c.SearchMovie(ctx, movieName, year, language, false, switchYear)
	}
	return &respResult, nil
}

// https://api.themoviedb.org/3/movie/{movie_id}
// 查询影片详情
func (c *Client) GetMovieDetail(ctx context.Context, movieID int64, language string) (*MovieDetail, error) {
	respResult := MovieDetail{}
	req := c.resty.R().SetContext(ctx).SetMethod("GET").SetResult(&respResult)
	// req.SetQueryParam("api_key", c.apiKey)
	resp, err := c.doRequest(fmt.Sprintf("/movie/%d?language=%s", movieID, language), req, MakeRequestConfig(2, 5, 5))
	if err != nil {
		helpers.TMDBLog.Errorf("获取电影详情失败：%+v", err)
		return nil, err
	}
	if !resp.IsStatusSuccess() {
		helpers.TMDBLog.Errorf("获取电影详情失败：%s", resp.String())
		return nil, fmt.Errorf("获取电影详情失败：%s", resp.String())
	}
	return &respResult, nil
}

func (c *Client) GetMoviePepoles(ctx context.Context, movieID int64, language string) (*PepolesRes, error) {
	respResult := PepolesRes{}
	req := c.resty.R().SetContext(ctx).SetMethod("GET").SetResult(&respResult)
	resp, err := c.doRequest(fmt.Sprintf("/movie/%d/credits?language=%s", movieID, language), req, MakeRequestConfig(2, 5, 5))
	if err != nil {
		helpers.TMDBLog.Errorf("获取电影演员失败：%+v", err)
		return nil, err
	}

	if !resp.IsStatusSuccess() {
		helpers.TMDBLog.Errorf("获取电影演员失败：%s", resp.String())
		return nil, fmt.Errorf("获取电影演员失败：%s", resp.String())
	}
	return &respResult, nil
}

// https://api.themoviedb.org/3/movie/{movie_id}/images
// 查询电影的图片
func (c *Client) GetMovieImages(ctx context.Context, movieID int64, language string) (*Images, error) {
	respResult := Images{}
	req := c.resty.R().SetContext(ctx).SetMethod("GET").SetResult(&respResult)
	// req.SetQueryParam("api_key", c.apiKey)
	resp, err := c.doRequest(fmt.Sprintf("/movie/%d/images?language=%s", movieID, language), req, MakeRequestConfig(2, 5, 5))
	if err != nil {
		helpers.TMDBLog.Errorf("获取电影图片失败：%+v", err)
		return nil, err
	}
	if !resp.IsStatusSuccess() {
		helpers.TMDBLog.Errorf("获取电影图片失败：%s", resp.String())
		return nil, fmt.Errorf("获取电影图片失败：%s", resp.String())
	}
	if len(respResult.Posters) == 0 && len(respResult.Backdrops) == 0 && len(respResult.Logos) == 0 {
		if language == "en-US" {
			language = "zh-CN"
		}
		// 重新查询
		resp, err := c.doRequest(fmt.Sprintf("/movie/%d/images?language=%s", movieID, language), req, MakeRequestConfig(2, 5, 5))
		if err != nil {
			helpers.TMDBLog.Errorf("获取电影图片失败：%+v", err)
			return nil, err
		}
		if !resp.IsStatusSuccess() {
			helpers.TMDBLog.Errorf("获取电影图片失败：%s", resp.String())
			return nil, fmt.Errorf("获取电影图片失败：%s", resp.String())
		}
	}
	return &respResult, nil
}

// https://api.themoviedb.org/3/movie/{movie_id}/keywords
// 查询电影的关键词（标签）
func (c *Client) GetMovieKeywords(ctx context.Context, movieID int64) (*MovieKeywords, error) {
	respResult := MovieKeywords{}
	req := c.resty.R().SetContext(ctx).SetMethod("GET").SetResult(&respResult)
	// req.SetQueryParam("api_key", c.apiKey)
	resp, err := c.doRequest(fmt.Sprintf("/movie/%d/keywords", movieID), req, MakeRequestConfig(2, 5, 5))
	if err != nil {
		helpers.TMDBLog.Errorf("获取电影关键词失败：%+v", err)
		return nil, err
	}
	if !resp.IsStatusSuccess() {
		helpers.TMDBLog.Errorf("获取电影关键词失败：%s", resp.String())
		return nil, fmt.Errorf("获取电影关键词失败：%s", resp.String())
	}
	return &respResult, nil
}

// https://api.themoviedb.org/3/movie/{movie_id}/release_dates
// 查询电影的发布日期，可以提取美国的分级
func (c *Client) GetReleasesDate(ctx context.Context, movieID int64) (*ReleasesDateResp, error) {
	respResult := ReleasesDateResp{}
	req := c.resty.R().SetContext(ctx).SetMethod("GET").SetResult(&respResult)
	// req.SetQueryParam("api_key", c.apiKey)
	resp, err := c.doRequest(fmt.Sprintf("/movie/%d/release_dates", movieID), req, MakeRequestConfig(2, 5, 5))
	// helpers.TMDBLog.Infof("获取电影发布日期响应：%s", resp.String())
	if err != nil {
		helpers.TMDBLog.Errorf("获取电影发布日期失败：%+v", err)
		return nil, err
	}
	if !resp.IsStatusSuccess() {
		helpers.TMDBLog.Errorf("获取电影发布日期失败：%s", resp.String())
		return nil, fmt.Errorf("获取电影发布日期失败：%s", resp.String())
	}
	return &respResult, nil
}
