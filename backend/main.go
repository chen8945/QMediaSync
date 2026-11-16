package main

import (
	"embed"

	"qmediasync/internal/app"
)

var Version string = "v0.0.1"
var PublishDate string = "2025-08-08"
var FANART_API_KEY = ""
var TMDB_ACCESS_TOKEN = ""
var TMDB_API_KEY = ""
var SC_API_KEY = ""
var OAuthRelayEncryptionKey = ""
var AppName string = "QMediaSync"

//go:embed assets/emby302.yaml
//go:embed assets/db_config.html
var embedFiles embed.FS

func main() {
	app.Run(app.Options{
		Version:                 Version,
		PublishDate:             PublishDate,
		AppName:                 AppName,
		FanartAPIKey:            FANART_API_KEY,
		TMDBAccessToken:         TMDB_ACCESS_TOKEN,
		TMDBAPIKey:              TMDB_API_KEY,
		SCAPIKey:                SC_API_KEY,
		OAuthRelayEncryptionKey: OAuthRelayEncryptionKey,
		Resources:               embedFiles,
	})
}
