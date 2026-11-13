package scrape

import (
	"context"

	"qmediasync/internal/models"
	"qmediasync/internal/openai"
)

type IdBase struct {
	tmdbImpl   TmdbImpl
	scrapePath *models.ScrapePath
	ctx        context.Context
	aiClient   *openai.Client
	aiPrompt   string
}
