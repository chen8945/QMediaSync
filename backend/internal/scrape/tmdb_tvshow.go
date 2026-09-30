package scrape

import (
	"context"
	"errors"
	"fmt"

	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
	"qmediasync/internal/tmdb"
)

// 从 TMDB 刮削元数据
type TmdbTvShowImpl struct {
	TmdbBase
}

func NewTmdbTvShowImpl(scrapePath *models.ScrapePath, ctx context.Context) *TmdbTvShowImpl {
	return &TmdbTvShowImpl{
		scrapePath: scrapePath,
		ctx:        ctx,
		Client:     models.GlobalScrapeSettings.GetTmdbClient(),
	}
}

// 检查电视剧是否存在
func (t *TmdbTvShowImpl) CheckByNameAndYear(name string, year int, switchYear bool) (string, int64, int, error) {
	// 查询电视剧详情
	tvShowDetail, err := t.Client.SearchTv(name, year, models.GlobalScrapeSettings.GetTmdbLanguage(), true)
	if err != nil {
		helpers.AppLogger.Errorf("查询 TMDB 电视剧详情失败，下次重试，失败原因：%v", err)
		return "", 0, 0, err
	}
	if len(tvShowDetail.Results) == 0 {
		// helpers.AppLogger.Errorf("识别结果 %s %d 查询失败, 失败原因：TMDB 没有数据", name, year)
		return "", 0, 0, errors.New("TMDB 没有数据")
	}
	if tvShowDetail.TotalResults > 1 {
		// 如果第一个数据的 name 等于 name，则认为是正确的
		if tvShowDetail.Results[0].Name == name {
			return tvShowDetail.Results[0].Name, tvShowDetail.Results[0].ID, helpers.ParseYearFromDate(tvShowDetail.Results[0].FirstAirDate), nil
		}
		errorStr := fmt.Sprintf("通过名称 %s、年份 %d 在 TMDB 查到多部电视剧，请手工重新识别并指定准确的 TMDB ID", name, year)
		helpers.AppLogger.Error(errorStr)
		return "", 0, 0, errors.New("多条记录")
	} else {
		return tvShowDetail.Results[0].Name, tvShowDetail.Results[0].ID, helpers.ParseYearFromDate(tvShowDetail.Results[0].FirstAirDate), nil
	}
}

// 去 TMDB 查询是否存在
func (t *TmdbTvShowImpl) CheckByTmdbId(tmdbId int64) (string, int, error) {
	// 查询详情
	tvDetail, err := t.Client.GetTvDetail(tmdbId, models.GlobalScrapeSettings.GetTmdbLanguage())
	if err != nil {
		helpers.AppLogger.Errorf("查询 TMDB 电视剧详情失败，下次重试，失败原因：%v", err)
		return "", 0, err
	}
	return tvDetail.Name, helpers.ParseYearFromDate(tvDetail.FirstAirDate), nil
}

// 检查季是否存在
func (t *TmdbTvShowImpl) CheckSeasonByTmdbId(tmdbId int64, seasonNumber int) (*tmdb.SeasonDetail, error) {
	// 查询季详情
	seasonDetail, err := t.Client.GetTvSeasonDetail(tmdbId, seasonNumber, models.GlobalScrapeSettings.GetTmdbLanguage())
	if err != nil {
		helpers.AppLogger.Errorf("查询 TMDB 电视剧季详情失败，下次重试，失败原因：%v", err)
		return nil, err
	}
	return seasonDetail, nil
}
