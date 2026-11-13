package scrape

import (
	"context"

	"qmediasync/internal/models"
	"qmediasync/internal/tmdb"
)

// 从 TMDB 刮削元数据
type TmdbBase struct {
	scrapePath *models.ScrapePath
	ctx        context.Context
	Client     *tmdb.Client
	settings   models.ScrapeSettings
}
