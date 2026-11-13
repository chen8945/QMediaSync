package tmdb

import (
	"context"
	"fmt"
	"net/url"

	"qmediasync/internal/helpers"
)

// https://api.themoviedb.org/3/search/tv
// 搜索 TV
type SearchTv struct {
	Adult            bool     `json:"adult"`             // 是否成人内容
	BackdropPath     string   `json:"backdrop_path"`     // 背景图片（选定语言的版本）
	GenreIDs         []int    `json:"genre_ids"`         // 流派 ID
	ID               int64    `json:"id"`                // ID
	Name             string   `json:"name"`              // 名称（选定语言的版本）
	OriginalName     string   `json:"original_name"`     // 原始名称
	Overview         string   `json:"overview"`          // 描述
	PosterPath       string   `json:"poster_path"`       // 封面图片（选定语言的版本）
	FirstAirDate     string   `json:"first_air_date"`    // 首播日期
	Popularity       float64  `json:"popularity"`        // 流行度
	OriginalLanguage string   `json:"original_language"` // 原始语言
	OriginCountry    []string `json:"origin_country"`    // 原始国家，国家代码数组
	VoteCount        int64    `json:"vote_count"`        // 投票数
	VoteAverage      float64  `json:"vote_average"`      // 平均评分
}

type SearchTvResponse struct {
	Page         int        `json:"page"`
	TotalResults int        `json:"total_results"`
	TotalPages   int        `json:"total_pages"`
	Results      []SearchTv `json:"results"`
}

type Season struct {
	AirDate      string  `json:"air_date"`
	EpisodeCount int     `json:"episode_count"`
	ID           int64   `json:"id"`
	Name         string  `json:"name"`
	Overview     string  `json:"overview"`
	PosterPath   string  `json:"poster_path"`
	SeasonNumber int     `json:"season_number"`
	VoteAverage  float64 `json:"vote_average"`
}

type TvNetwork struct {
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	LogoPath      string `json:"logo_path"`      // 播放平台 logo
	OriginCountry string `json:"origin_country"` // 原始国家
}

type TvDetail struct {
	SearchTv
	EpisodeRunTime      []int               `json:"episode_run_time"`     // 每集时长
	Genres              []Genre             `json:"genres"`               // 流派
	LastAirDate         string              `json:"last_air_date"`        // 最近一集播放日期
	Networks            []TvNetwork         `json:"networks"`             // 播放平台
	NumberOfEpisodes    int                 `json:"number_of_episodes"`   // 总集数
	NumberOfSeasons     int                 `json:"number_of_seasons"`    // 总季数
	ProductionCompanies []ProductionCompany `json:"production_companies"` // 生产公司
	ProductionCountries []Country           `json:"production_countries"` // 生产国家
	Seasons             []Season            `json:"seasons"`              // 季列表
	SpokenLanguages     []Language          `json:"spoken_languages"`     // 语言
	Status              string              `json:"status"`               // 状态
	Tagline             string              `json:"tagline"`              // 标语
	Type                string              `json:"type"`                 // 类型
	Homepage            string              `json:"homepage"`             // 首页
}

type TvKeywords struct {
	ID      int64     `json:"id"`      // 影片 ID
	Results []Keyword `json:"results"` // 关键词列表
}

type Episode struct {
	AirDate        string  `json:"air_date"`        // 播出时间
	EpisodeNumber  int     `json:"episode_number"`  // 集编号
	EpisodeType    string  `json:"episode_type"`    // 集类型
	ID             int64   `json:"id"`              // 集 ID
	Name           string  `json:"name"`            // 集名称
	Overview       string  `json:"overview"`        // 集描述
	ProductionCode string  `json:"production_code"` // 集生产代码
	Runtime        int     `json:"runtime"`         // 集时长
	SeasonNumber   int     `json:"season_number"`   // 集所属季编号
	ShowID         int64   `json:"show_id"`         // 集所属 TV ID
	StillPath      string  `json:"still_path"`      // 集封面图片
	VoteAverage    float64 `json:"vote_average"`    // 集平均评分
	VoteCount      int64   `json:"vote_count"`      // 集投票数
	Crew           []Crew  `json:"crew"`            // 集 crew 列表
	Cast           []Cast  `json:"cast"`            // 集 cast 列表
}

type SeasonDetail struct {
	ID           int64  `json:"id"`            // 季 ID
	Name         string `json:"name"`          // 季名称
	Overview     string `json:"overview"`      // 季描述
	AirDate      string `json:"air_date"`      // 播出时间
	EpisodeCount int    `json:"episode_count"` // 集数
	// Episodes     []Episode   `json:"episodes"`      // 集列表，不收集集列表
	PosterPath   string      `json:"poster_path"`   // 季封面图片
	SeasonNumber int         `json:"season_number"` // 季编号
	VoteAverage  float64     `json:"vote_average"`  // 季平均评分
	Network      []TvNetwork `json:"network"`       // 播放平台
}

func (c *Client) SearchTv(ctx context.Context, tvName string, year int, language string, switchLanguage bool) (*SearchTvResponse, error) {
	var respResult SearchTvResponse = SearchTvResponse{}
	req := c.resty.R().SetContext(ctx).SetMethod("GET").SetResult(&respResult)
	if year > 0 {
		req.SetQueryParam("first_air_date_year", fmt.Sprintf("%d", year))
	}
	if language != "" {
		req.SetQueryParam("language", language)
	}
	resp, err := c.doRequest(fmt.Sprintf("/search/tv?query=%s", url.QueryEscape(tvName)), req, MakeRequestConfig(2, 5, 5))
	if err != nil {
		helpers.TMDBLog.Errorf("搜索电视剧失败：%+v", err)
		return nil, err
	}
	if !resp.IsStatusSuccess() {
		helpers.TMDBLog.Errorf("搜索电视剧失败：%s", resp.String())
		return nil, fmt.Errorf("搜索电视剧失败：%s", resp.String())
	}
	if len(respResult.Results) == 0 && switchLanguage {
		// 换一种语言搜索
		if language == "en-US" {
			language = "zh-CN"
		} else {
			language = "en-US"
		}
		return c.SearchTv(ctx, tvName, year, language, false)
	}
	return &respResult, nil
}

func (c *Client) GetTvDetail(ctx context.Context, tvID int64, language string) (*TvDetail, error) {
	respResult := TvDetail{}
	req := c.resty.R().SetContext(ctx).SetMethod("GET").SetResult(&respResult)
	// req.SetQueryParam("api_key", c.apiKey)
	resp, err := c.doRequest(fmt.Sprintf("/tv/%d?language=%s", tvID, language), req, MakeRequestConfig(2, 5, 5))
	if err != nil {
		helpers.TMDBLog.Errorf("获取电视剧详情失败：%+v", err)
		return nil, err
	}
	if !resp.IsStatusSuccess() {
		helpers.TMDBLog.Errorf("获取电视剧详情失败：%s", resp.String())
		return nil, fmt.Errorf("获取电视剧详情失败：%s", resp.String())
	}
	return &respResult, nil
}

// https://api.themoviedb.org/3/tv/{series_id}/images
// 查询电视剧的图片
func (c *Client) GetTvImages(ctx context.Context, tvId int64, langauge string) (*Images, error) {
	respResult := Images{}
	req := c.resty.R().SetContext(ctx).SetMethod("GET").SetResult(&respResult)
	// req.SetQueryParam("api_key", c.apiKey)
	resp, err := c.doRequest(fmt.Sprintf("/tv/%d/images?language=%s", tvId, langauge), req, MakeRequestConfig(2, 5, 5))
	if err != nil {
		helpers.TMDBLog.Errorf("获取电视剧图片失败：%+v", err)
		return nil, err
	}
	if !resp.IsStatusSuccess() {
		helpers.TMDBLog.Errorf("获取电视剧图片失败：%s", resp.String())
		return nil, fmt.Errorf("获取电视剧图片失败：%s", resp.String())
	}
	if len(respResult.Posters) == 0 && len(respResult.Backdrops) == 0 && len(respResult.Logos) == 0 {
		if langauge == "en-US" {
			langauge = "zh-CN"
		}
		// 重新查询
		resp, err := c.doRequest(fmt.Sprintf("/tv/%d/images?language=%s", tvId, langauge), req, MakeRequestConfig(2, 5, 5))
		if err != nil {
			helpers.TMDBLog.Errorf("获取电视剧图片失败：%+v", err)
			return nil, err
		}
		if !resp.IsStatusSuccess() {
			helpers.TMDBLog.Errorf("获取电视剧图片失败：%s", resp.String())
			return nil, fmt.Errorf("获取电视剧图片失败：%s", resp.String())
		}
	}
	return &respResult, nil
}

// https://api.themoviedb.org/3/tv/{series_id}/credits
// 查询电视剧的演职人员
func (c *Client) GetTvCredits(ctx context.Context, tvId int64, langauge string) (*PepolesRes, error) {
	respResult := PepolesRes{}
	req := c.resty.R().SetContext(ctx).SetMethod("GET").SetResult(&respResult)
	// req.SetQueryParam("api_key", c.apiKey)
	resp, err := c.doRequest(fmt.Sprintf("/tv/%d/credits?language=%s", tvId, langauge), req, MakeRequestConfig(2, 5, 5))
	if err != nil {
		helpers.TMDBLog.Errorf("获取电视剧演职人员失败：%+v", err)
		return nil, err
	}
	if !resp.IsStatusSuccess() {
		helpers.TMDBLog.Errorf("获取电视剧演职人员失败：%s", resp.String())
		return nil, fmt.Errorf("获取电视剧演职人员失败：%s", resp.String())
	}
	return &respResult, nil
}

// https://api.themoviedb.org/3/tv/{series_id}/keywords
// 查询电视剧的关键词（标签）
func (c *Client) GetTvKeywords(ctx context.Context, tvId int64) (*TvKeywords, error) {
	respResult := TvKeywords{}
	req := c.resty.R().SetContext(ctx).SetMethod("GET").SetResult(&respResult)
	// req.SetQueryParam("api_key", c.apiKey)
	resp, err := c.doRequest(fmt.Sprintf("/tv/%d/keywords", tvId), req, MakeRequestConfig(2, 5, 5))
	if err != nil {
		helpers.TMDBLog.Errorf("获取电视剧关键词失败：%+v", err)
		return nil, err
	}
	if !resp.IsStatusSuccess() {
		helpers.TMDBLog.Errorf("获取电视剧关键词失败：%s", resp.String())
		return nil, fmt.Errorf("获取电视剧关键词失败：%s", resp.String())
	}
	return &respResult, nil
}

func (c *Client) GetTvSeasonDetail(ctx context.Context, tvId int64, seasonNumber int, langauge string) (*SeasonDetail, error) {
	respResult := SeasonDetail{}
	req := c.resty.R().SetContext(ctx).SetMethod("GET").SetResult(&respResult)
	// req.SetQueryParam("api_key", c.apiKey)
	resp, err := c.doRequest(fmt.Sprintf("/tv/%d/season/%d?language=%s", tvId, seasonNumber, langauge), req, MakeRequestConfig(2, 5, 5))
	if err != nil {
		helpers.TMDBLog.Errorf("获取电视剧季详情失败：%+v", err)
		return nil, err
	}
	if !resp.IsStatusSuccess() {
		helpers.TMDBLog.Errorf("获取电视剧季详情失败：%s", resp.String())
		return nil, fmt.Errorf("获取电视剧季详情失败：%s", resp.String())
	}
	return &respResult, nil
}

// https://api.themoviedb.org/3/tv/{series_id}/season/{season_number}/credits
// 查询季的演职人员
func (c *Client) GetTvSeasonCredits(ctx context.Context, tvId int64, seasonNumber int, langauge string) (*PepolesRes, error) {
	respResult := PepolesRes{}
	req := c.resty.R().SetContext(ctx).SetMethod("GET").SetResult(&respResult)
	// req.SetQueryParam("api_key", c.apiKey)
	resp, err := c.doRequest(fmt.Sprintf("/tv/%d/season/%d/credits?language=%s", tvId, seasonNumber, langauge), req, MakeRequestConfig(2, 5, 5))
	if err != nil {
		helpers.TMDBLog.Errorf("获取电视剧季演职人员失败：%+v", err)
		return nil, err
	}
	if !resp.IsStatusSuccess() {
		helpers.TMDBLog.Errorf("获取电视剧季演职人员失败：%s", resp.String())
		return nil, fmt.Errorf("获取电视剧季演职人员失败：%s", resp.String())
	}
	return &respResult, nil
}

// https://api.themoviedb.org/3/tv/{series_id}/season/{season_number}/images
// 查询季的图片
func (c *Client) GetTvSeasonImages(ctx context.Context, tvId int64, seasonNumber int, langauge string) (*Images, error) {
	respResult := Images{}
	req := c.resty.R().SetContext(ctx).SetMethod("GET").SetResult(&respResult)
	// req.SetQueryParam("api_key", c.apiKey)
	resp, err := c.doRequest(fmt.Sprintf("/tv/%d/season/%d/images?language=%s", tvId, seasonNumber, langauge), req, MakeRequestConfig(2, 5, 5))
	if err != nil {
		helpers.TMDBLog.Errorf("获取电视剧季图片失败：%+v", err)
		return nil, err
	}
	if !resp.IsStatusSuccess() {
		helpers.TMDBLog.Errorf("获取电视剧季图片失败：%s", resp.String())
		return nil, fmt.Errorf("获取电视剧季图片失败：%s", resp.String())
	}
	return &respResult, nil
}

func (c *Client) GetTvEpisodeDetail(ctx context.Context, tvId int64, seasonNumber int, episodeNumber int, langauge string) (*Episode, error) {
	respResult := Episode{}
	req := c.resty.R().SetContext(ctx).SetMethod("GET").SetResult(&respResult)
	resp, err := c.doRequest(fmt.Sprintf("/tv/%d/season/%d/episode/%d?language=%s", tvId, seasonNumber, episodeNumber, langauge), req, MakeRequestConfig(2, 5, 5))
	if err != nil {
		helpers.TMDBLog.Errorf("获取电视剧集详情失败：%+v", err)
		return nil, err
	}
	if !resp.IsStatusSuccess() {
		helpers.TMDBLog.Errorf("获取电视剧集详情失败：%s", resp.String())
		return nil, fmt.Errorf("获取电视剧集详情失败：%s", resp.String())
	}
	respResult.Cast = make([]Cast, 0)
	respResult.Crew = make([]Crew, 0)
	return &respResult, nil
}

// https://api.themoviedb.org/3/tv/{series_id}/season/{season_number}/episode/{episode_number}/credits
// 查询集的演职人员
func (c *Client) GetTvEpisodeCredits(ctx context.Context, tvId int64, seasonNumber int, episodeNumber int, langauge string) (*PepolesRes, error) {
	respResult := PepolesRes{}
	req := c.resty.R().SetContext(ctx).SetMethod("GET").SetResult(&respResult)
	// req.SetQueryParam("api_key", c.apiKey)
	resp, err := c.doRequest(fmt.Sprintf("/tv/%d/season/%d/episode/%d/credits?language=%s", tvId, seasonNumber, episodeNumber, langauge), req, MakeRequestConfig(2, 5, 5))
	if err != nil {
		helpers.TMDBLog.Errorf("获取电视剧集演职人员失败：%+v", err)
		return nil, err
	}
	if !resp.IsStatusSuccess() {
		helpers.TMDBLog.Errorf("获取电视剧集演职人员失败：%s", resp.String())
		return nil, fmt.Errorf("获取电视剧集演职人员失败：%s", resp.String())
	}
	return &respResult, nil
}
