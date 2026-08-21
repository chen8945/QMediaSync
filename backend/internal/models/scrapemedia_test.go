package models

import (
	"testing"

	"qmediasync/internal/helpers"
)

func createTestMovieData() *ScrapeMediaFile {
	return &ScrapeMediaFile{
		Name:            "星际穿越",
		Year:            2014,
		TmdbId:          157336,
		Resolution:      "1080p",
		ResolutionLevel: "FHD",
		VideoExt:        ".mkv",
		VideoCodec: &VideoCodec{
			Codec:   "h264",
			Bitrate: 8000000,
		},
		AudioCodec: []*AudioCodec{
			{
				Codec: "aac",
			},
		},
		Media: &Media{
			Name:             "星际穿越",
			OriginalName:     "Interstellar",
			OriginalLanguage: "en",
			ImdbId:           "tt0816692",
			Runtime:          169,
			Overview:         "一群探险家利用虫洞进行星际旅行",
			VoteAverage:      8.6,
			Actors: []helpers.Actor{
				{Name: "马修·麦康纳", Role: "Cooper"},
				{Name: "安妮·海瑟薇", Role: "Brand"},
			},
			Num: "ABCD-1234",
		},
	}
}

func createTestMovieDataWithMultipleActors() *ScrapeMediaFile {
	return &ScrapeMediaFile{
		Name:            "复仇者联盟4：终局之战",
		Year:            2019,
		TmdbId:          299534,
		Resolution:      "2160p",
		ResolutionLevel: "UHD",
		VideoExt:        ".mkv",
		VideoCodec: &VideoCodec{
			Codec:   "hevc",
			Bitrate: 15000000,
		},
		AudioCodec: []*AudioCodec{
			{
				Codec: "dts",
			},
		},
		Media: &Media{
			Name:         "复仇者联盟4：终局之战",
			OriginalName: "Avengers: Endgame",
			ImdbId:       "tt4154796",
			Runtime:      181,
			VoteAverage:  8.4,
			Actors: []helpers.Actor{
				{Name: "小罗伯特·唐尼", Role: "Tony Stark"},
				{Name: "克里斯·埃文斯", Role: "Steve Rogers"},
				{Name: "斯嘉丽·约翰逊", Role: "Natasha Romanoff"},
				{Name: "克里斯·海姆斯沃斯", Role: "Thor"},
			},
		},
	}
}

func createTestMovieDataNoActors() *ScrapeMediaFile {
	return &ScrapeMediaFile{
		Name:            "小众电影",
		Year:            2023,
		TmdbId:          0,
		Resolution:      "720p",
		ResolutionLevel: "HD",
		VideoExt:        ".mp4",
		VideoCodec: &VideoCodec{
			Codec:   "h264",
			Bitrate: 4000000,
		},
		Media: &Media{
			Name:         "小众电影",
			OriginalName: "Niche Movie",
			Runtime:      90,
			VoteAverage:  7.2,
			Actors:       []helpers.Actor{},
		},
	}
}

func createTestTVShowData() *ScrapeMediaFile {
	return &ScrapeMediaFile{
		Name:            "猎魔人",
		Year:            2019,
		TmdbId:          71914,
		Resolution:      "1080p",
		ResolutionLevel: "FHD",
		VideoExt:        ".mkv",
		SeasonNumber:    2,
		EpisodeNumber:   8,
		MediaType:       MediaTypeTvShow,
		Media: &Media{
			Name:         "猎魔人",
			OriginalName: "The Witcher",
			ImdbId:       "tt5180504",
			Runtime:      60,
			VoteAverage:  8.2,
			Actors: []helpers.Actor{
				{Name: "亨利·卡维尔", Role: "Geralt of Rivia"},
				{Name: "弗蕾娅·艾伦", Role: "Ciri"},
			},
		},
		MediaEpisode: &MediaEpisode{
			EpisodeName: "穿越时空的界限",
			Year:        2020,
		},
		MediaSeason: &MediaSeason{
			Year: 2020,
		},
	}
}

func createTestTVShowDataNoEpisodeInfo() *ScrapeMediaFile {
	return &ScrapeMediaFile{
		Name:            "未识别剧集",
		Year:            2024,
		TmdbId:          888888,
		Resolution:      "2160p",
		ResolutionLevel: "UHD",
		VideoExt:        ".mkv",
		SeasonNumber:    1,
		EpisodeNumber:   0,
		MediaType:       MediaTypeTvShow,
		Media: &Media{
			Name:         "未识别剧集",
			OriginalName: "Unknown Show",
			Runtime:      45,
			VoteAverage:  7.5,
			Actors:       []helpers.Actor{},
		},
	}
}

func TestOldSyntax_BasicMovie(t *testing.T) {
	sm := createTestMovieData()

	tests := []struct {
		name     string
		template string
		expected string
	}{
		{
			name:     "基础模板",
			template: "{title} ({year})",
			expected: "星际穿越 (2014)",
		},
		{
			name:     "带分辨率",
			template: "{title} ({year}) [{resolution}]",
			expected: "星际穿越 (2014) [1080p]",
		},
		{
			name:     "带分辨率等级",
			template: "{title} ({year}) {resolution_level}",
			expected: "星际穿越 (2014) FHD",
		},
		{
			name:     "带 TMDB ID",
			template: "{title} {tmdb_id}",
			expected: "星际穿越 {tmdbid-157336}",
		},
		{
			name:     "带演员",
			template: "{title} - {actors}",
			expected: "星际穿越 - 马修·麦康纳, 安妮·海瑟薇",
		},
		{
			name:     "带番号",
			template: "{title} [{num}]",
			expected: "星际穿越 [ABCD-1234]",
		},
		{
			name:     "完整模板",
			template: "{title} ({year}) [{resolution}] {actors} - {tmdb_id}",
			expected: "星际穿越 (2014) [1080p] 马修·麦康纳, 安妮·海瑟薇 - {tmdbid-157336}",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := sm.GenerateNameByTemplate(tt.template)
			if result != tt.expected {
				t.Errorf("模板 '%s' 生成失败\n期望: %s\n实际: %s", tt.template, tt.expected, result)
			}
		})
	}
}

func TestOldSyntax_TVShow(t *testing.T) {
	sm := createTestTVShowData()

	tests := []struct {
		name     string
		template string
		expected string
	}{
		{
			name:     "基础电视剧模板",
			template: "{title} - S{season_number}E{episode_number}",
			expected: "猎魔人 - S2E8",
		},
		{
			name:     "带季集格式",
			template: "{title} {season_episode}",
			expected: "猎魔人 S02E08",
		},
		{
			name:     "带集标题",
			template: "{title} - {season_episode} - {episode_name}",
			expected: "猎魔人 - S02E08 - 穿越时空的界限",
		},
		{
			name:     "完整电视剧模板",
			template: "{title} ({year}) S{season_number}E{episode_number} - {episode_name}",
			expected: "猎魔人 (2019) S2E8 - 穿越时空的界限",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := sm.GenerateNameByTemplate(tt.template)
			if result != tt.expected {
				t.Errorf("模板 '%s' 生成失败\n期望: %s\n实际: %s", tt.template, tt.expected, result)
			}
		})
	}
}

func TestOldSyntax_MultipleActors(t *testing.T) {
	sm := createTestMovieDataWithMultipleActors()

	tests := []struct {
		name     string
		template string
		expected string
	}{
		{
			name:     "多人演员（3 个以上）",
			template: "{title} - {actors}",
			expected: "复仇者联盟4：终局之战 - 多人演员",
		},
		{
			name:     "完整信息",
			template: "{title} ({year}) [{resolution}] {actors}",
			expected: "复仇者联盟4：终局之战 (2019) [2160p] 多人演员",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := sm.GenerateNameByTemplate(tt.template)
			if result != tt.expected {
				t.Errorf("模板 '%s' 生成失败\n期望: %s\n实际: %s", tt.template, tt.expected, result)
			}
		})
	}
}

func TestOldSyntax_EmptyFields(t *testing.T) {
	sm := createTestMovieDataNoActors()

	tests := []struct {
		name     string
		template string
		expected string
	}{
		{
			name:     "无演员",
			template: "{title} ({year}) {actors}",
			expected: "小众电影 (2023) ",
		},
		{
			name:     "无 TMDB ID",
			template: "{title} {tmdb_id}",
			expected: "小众电影 ",
		},
		{
			name:     "混合空字段",
			template: "{title} ({year}) {actors} - {tmdb_id}",
			expected: "小众电影 (2023)  - ",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := sm.GenerateNameByTemplate(tt.template)
			if result != tt.expected {
				t.Errorf("模板 '%s' 生成失败\n期望: '%s'\n实际: '%s'", tt.template, tt.expected, result)
			}
		})
	}
}

func TestNewSyntax_BasicMovie(t *testing.T) {
	sm := createTestMovieData()

	tests := []struct {
		name     string
		template string
		expected string
	}{
		{
			name:     "基础模板",
			template: "{{title}} ({{year}})",
			expected: "星际穿越 (2014)",
		},
		{
			name:     "带分辨率",
			template: "{{title}} ({{year}}) [{{videoFormat}}]",
			expected: "星际穿越 (2014) [1080p]",
		},
		{
			name:     "带分辨率等级",
			template: "{{title}} ({{year}}) {{edition}}",
			expected: "星际穿越 (2014) FHD",
		},
		{
			name:     "带 TMDB ID",
			template: "{{title}} {{tmdbid}}",
			expected: "星际穿越 157336",
		},
		{
			name:     "带演员",
			template: "{{title}} - {{actors}}",
			expected: "星际穿越 - 马修·麦康纳, 安妮·海瑟薇",
		},
		{
			name:     "带原始标题",
			template: "{{title}} / {{original_title}}",
			expected: "星际穿越 / Interstellar",
		},
		{
			name:     "带IMDB ID",
			template: "{{title}} [{{imdbid}}]",
			expected: "星际穿越 [tt0816692]",
		},
		{
			name:     "带运行时间",
			template: "{{title}} ({{runtime}}min)",
			expected: "星际穿越 (169min)",
		},
		{
			name:     "带评分",
			template: "{{title}} - {{vote_average}}",
			expected: "星际穿越 - 8.600000",
		},
		{
			name:     "完整模板",
			template: "{{title}} ({{year}}) [{{videoFormat}}] {{actors}} - {{tmdbid}}",
			expected: "星际穿越 (2014) [1080p] 马修·麦康纳, 安妮·海瑟薇 - 157336",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := sm.GenerateNameByTemplate(tt.template)
			if result != tt.expected {
				t.Errorf("模板 '%s' 生成失败\n期望: %s\n实际: %s", tt.template, tt.expected, result)
			}
		})
	}
}

func TestNewSyntax_TVShow(t *testing.T) {
	sm := createTestTVShowData()

	tests := []struct {
		name     string
		template string
		expected string
	}{
		{
			name:     "基础电视剧模板",
			template: "{{title}} - S{{season}}E{{episode}}",
			expected: "猎魔人 - S2E8",
		},
		{
			name:     "带季集格式",
			template: "{{title}} {{season_episode}}",
			expected: "猎魔人 S02E08",
		},
		{
			name:     "带集标题",
			template: "{{title}} - {{season_episode}} - {{episode_title}}",
			expected: "猎魔人 - S02E08 - 穿越时空的界限",
		},
		{
			name:     "带季年份",
			template: "{{title}} S{{season}} ({{season_year}})",
			expected: "猎魔人 S2 (2020)",
		},
		{
			name:     "完整电视剧模板",
			template: "{{title}} ({{year}}) S{{season}}E{{episode}} - {{episode_title}} [{{videoFormat}}]",
			expected: "猎魔人 (2019) S2E8 - 穿越时空的界限 [1080p]",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := sm.GenerateNameByTemplate(tt.template)
			if result != tt.expected {
				t.Errorf("模板 '%s' 生成失败\n期望: %s\n实际: %s", tt.template, tt.expected, result)
			}
		})
	}
}

func TestNewSyntax_ConditionalLogic(t *testing.T) {
	sm := createTestMovieData()

	tests := []struct {
		name     string
		template string
		expected string
	}{
		{
			name:     "条件年份（有值）",
			template: "{{title}}{% if year %} ({{year}}){% endif %}",
			expected: "星际穿越 (2014)",
		},
		{
			name:     "条件演员（有值）",
			template: "{{title}}{% if actors %} - {{actors}}{% endif %}",
			expected: "星际穿越 - 马修·麦康纳, 安妮·海瑟薇",
		},
		{
			name:     "条件分辨率（有值）",
			template: "{{title}}{% if videoFormat %} [{{videoFormat}}]{% endif %}",
			expected: "星际穿越 [1080p]",
		},
		{
			name:     "多个条件",
			template: "{{title}}{% if year %} ({{year}}){% endif %}{% if actors %} - {{actors}}{% endif %}",
			expected: "星际穿越 (2014) - 马修·麦康纳, 安妮·海瑟薇",
		},
		{
			name:     "条件 TMDB ID（有值）",
			template: "{{title}}{% if tmdbid %} [{{tmdbid}}]{% endif %}",
			expected: "星际穿越 [157336]",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := sm.GenerateNameByTemplate(tt.template)
			if result != tt.expected {
				t.Errorf("模板 '%s' 生成失败\n期望: %s\n实际: %s", tt.template, tt.expected, result)
			}
		})
	}
}

func TestNewSyntax_ConditionalLogic_EmptyFields(t *testing.T) {
	sm := createTestMovieDataNoActors()

	tests := []struct {
		name     string
		template string
		expected string
	}{
		{
			name:     "条件演员（无值）",
			template: "{{title}}{% if actors %} - {{actors}}{% endif %}",
			expected: "小众电影",
		},
		{
			name:     "条件演员带连接符（无值）",
			template: "{{title}}{% if actors %} - {{actors}}-{% endif %}",
			expected: "小众电影",
		},
		{
			name:     "条件年份带连接符（有值）",
			template: "{{title}}{% if year %} - {{year}}-{% endif %}",
			expected: "小众电影 - 2023-",
		},
		{
			name:     "多个条件（部分有值）",
			template: "{{title}}{% if year %} ({{year}}){% endif %}{% if actors %} - {{actors}}-{% endif %}",
			expected: "小众电影 (2023)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := sm.GenerateNameByTemplate(tt.template)
			if result != tt.expected {
				t.Errorf("模板 '%s' 生成失败\n期望: %s\n实际: %s", tt.template, tt.expected, result)
			}
		})
	}
}

func TestNewSyntax_TVShow_ConditionalLogic(t *testing.T) {
	sm := createTestTVShowData()

	tests := []struct {
		name     string
		template string
		expected string
	}{
		{
			name:     "条件季集（有值）",
			template: "{{title}}{% if season_episode %} {{season_episode}}{% endif %}",
			expected: "猎魔人 S02E08",
		},
		{
			name:     "条件集标题（有值）",
			template: "{{title}} S{{season}}E{{episode}}{% if episode_title %} - {{episode_title}}{% endif %}",
			expected: "猎魔人 S2E8 - 穿越时空的界限",
		},
		{
			name:     "条件分辨率（有值）",
			template: "{{title}} S{{season}}E{{episode}}{% if videoFormat %} [{{videoFormat}}]{% endif %}",
			expected: "猎魔人 S2E8 [1080p]",
		},
		{
			name:     "完整条件模板",
			template: "{{title}}{% if year %} ({{year}}){% endif %} S{{season}}E{{episode}}{% if episode_title %} - {{episode_title}}{% endif %}{% if videoFormat %} [{{videoFormat}}]{% endif %}",
			expected: "猎魔人 (2019) S2E8 - 穿越时空的界限 [1080p]",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := sm.GenerateNameByTemplate(tt.template)
			if result != tt.expected {
				t.Errorf("模板 '%s' 生成失败\n期望: %s\n实际: %s", tt.template, tt.expected, result)
			}
		})
	}
}

func TestNewSyntax_TVShow_EmptyEpisodeInfo(t *testing.T) {
	sm := createTestTVShowDataNoEpisodeInfo()

	tests := []struct {
		name     string
		template string
		expected string
	}{
		{
			name:     "条件集号（无值）",
			template: "{{title}} S{{season}}{% if episode %}E{{episode}}{% endif %}",
			expected: "未识别剧集 S1",
		},
		{
			name:     "条件季集（无值）",
			template: "{{title}}{% if season_episode %} {{season_episode}}{% endif %}",
			expected: "未识别剧集",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := sm.GenerateNameByTemplate(tt.template)
			if result != tt.expected {
				t.Errorf("模板 '%s' 生成失败\n期望: %s\n实际: %s", tt.template, tt.expected, result)
			}
		})
	}
}

func TestNewSyntax_MoviePilotCompatible(t *testing.T) {
	sm := createTestTVShowData()

	tests := []struct {
		name     string
		template string
		expected string
	}{
		{
			name:     "MoviePilot 默认电视剧模板",
			template: "{{title}}{% if year %} ({{year}}){% endif %}/Season {{season}}/{{title}} - {{season_episode}}{{fileExt}}",
			expected: "猎魔人 (2019)/Season 2/猎魔人 - S02E08.mkv",
		},
		{
			name:     "带演员的条件模板",
			template: "{{title}}{% if year %} ({{year}}){% endif %}{% if actors %} - {{actors}}-{% endif %}",
			expected: "猎魔人 (2019) - 亨利·卡维尔, 弗蕾娅·艾伦-",
		},
		{
			name:     "带分辨率的条件模板",
			template: "{{title}}{% if videoFormat %} [{{videoFormat}}]{% endif %}{% if edition %} {{edition}}{% endif %}",
			expected: "猎魔人 [1080p] FHD",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := sm.GenerateNameByTemplate(tt.template)
			if result != tt.expected {
				t.Errorf("模板 '%s' 生成失败\n期望: %s\n实际: %s", tt.template, tt.expected, result)
			}
		})
	}
}

func TestBackwardCompatibility(t *testing.T) {
	sm := createTestMovieData()

	templates := map[string]string{
		"旧语法基础":   "{title} ({year})",
		"旧语法带分辨率": "{title} ({year}) [{resolution}]",
		"旧语法带演员":  "{title} - {actors}",
		"旧语法完整":   "{title} ({year}) [{resolution}] {actors}",
		"新语法基础":   "{{title}} ({{year}})",
		"新语法带分辨率": "{{title}} ({{year}}) [{{videoFormat}}]",
		"新语法带演员":  "{{title}} - {{actors}}",
		"新语法完整":   "{{title}} ({{year}}) [{{videoFormat}}] {{actors}}",
		"新语法条件":   "{{title}}{% if year %} ({{year}}){% endif %}{% if actors %} - {{actors}}{% endif %}",
	}

	for name, template := range templates {
		t.Run(name, func(t *testing.T) {
			result := sm.GenerateNameByTemplate(template)
			if result == "" {
				t.Errorf("模板 '%s' 返回空字符串", template)
			}
			t.Logf("模板: %s\n结果: %s\n", template, result)
		})
	}
}

func TestSyntaxDetection(t *testing.T) {
	tests := []struct {
		name     string
		template string
		isNew    bool
	}{
		{
			name:     "旧语法-简单",
			template: "{title} ({year})",
			isNew:    false,
		},
		{
			name:     "旧语法-复杂",
			template: "{title} ({year}) [{resolution}] {actors} - {tmdb_id}",
			isNew:    false,
		},
		{
			name:     "新语法-简单",
			template: "{{title}} ({{year}})",
			isNew:    true,
		},
		{
			name:     "新语法-变量",
			template: "{{title}} {{year}} {{videoFormat}}",
			isNew:    true,
		},
		{
			name:     "新语法-条件",
			template: "{{title}}{% if year %} ({{year}}){% endif %}",
			isNew:    true,
		},
		{
			name:     "新语法-多条件",
			template: "{{title}}{% if year %} ({{year}}){% endif %}{% if actors %} - {{actors}}{% endif %}",
			isNew:    true,
		},
	}

	sm := createTestMovieData()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := sm.isNewTemplateSyntax(tt.template)
			if result != tt.isNew {
				t.Errorf("模板 '%s' 检测失败\n期望: %v\n实际: %v", tt.template, tt.isNew, result)
			}
		})
	}
}

func TestSanitizeTemplateName(t *testing.T) {
	tests := []struct {
		name     string
		rendered string
		expected string
	}{
		{
			name:     "正常多层级",
			rendered: "某演员/ABC-123",
			expected: "某演员/ABC-123",
		},
		{
			name:     "首层级为空",
			rendered: "/ABC-123",
			expected: "ABC-123",
		},
		{
			name:     "末层级为空",
			rendered: "某演员/",
			expected: "某演员",
		},
		{
			name:     "层级只有空白",
			rendered: "某演员/   /ABC-123",
			expected: "某演员/ABC-123",
		},
		{
			name:     "层级为当前目录",
			rendered: "./ABC-123",
			expected: "ABC-123",
		},
		{
			name:     "层级为上级目录",
			rendered: "某演员/../ABC-123",
			expected: "某演员/ABC-123",
		},
		{
			name:     "全部为上级目录",
			rendered: "../..",
			expected: "",
		},
		{
			name:     "全部为空",
			rendered: "/",
			expected: "",
		},
		{
			name:     "去掉首尾空白",
			rendered: "  某演员 / ABC-123  ",
			expected: "某演员/ABC-123",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if result := sanitizeTemplateName(tt.rendered); result != tt.expected {
				t.Errorf("清理 '%s' 失败\n期望: %s\n实际: %s", tt.rendered, tt.expected, result)
			}
		})
	}
}

func TestGenerateNameByTemplateOrKeep(t *testing.T) {
	other := func() *ScrapeMediaFile {
		return &ScrapeMediaFile{
			MediaType:     MediaTypeOther,
			ScrapeType:    ScrapeTypeOnlyRename,
			VideoFilename: "ABC-123.mp4",
			Name:          "ABC-123 测试标题",
			Media:         &Media{Name: "ABC-123 测试标题"},
		}
	}

	tests := []struct {
		name     string
		build    func() *ScrapeMediaFile
		template string
		fallback string
		expected string
	}{
		{
			name: "变量齐全时使用模板结果",
			build: func() *ScrapeMediaFile {
				sm := other()
				sm.Media.Num = "ABC-123"
				sm.Media.Actors = []helpers.Actor{{Name: "某演员"}}
				return sm
			},
			template: "{actors}/{num}",
			fallback: "ABC-123",
			expected: "某演员/ABC-123",
		},
		{
			name: "番号为空时丢弃空层级",
			build: func() *ScrapeMediaFile {
				sm := other()
				sm.Media.Actors = []helpers.Actor{{Name: "某演员"}}
				return sm
			},
			template: "{actors}/{num}",
			fallback: "ABC-123",
			expected: "某演员",
		},
		{
			name:     "变量全部为空时保留原名称",
			build:    other,
			template: "{actors}/{num}",
			fallback: "ABC-123",
			expected: "ABC-123",
		},
		{
			name:     "新语法变量全部为空时保留原名称",
			build:    other,
			template: "{{actors}}/{{num}}",
			fallback: "ABC-123",
			expected: "ABC-123",
		},
		{
			name:     "原名称也为空时退回视频文件名",
			build:    other,
			template: "{num}",
			fallback: "",
			expected: "ABC-123",
		},
		{
			name:     "原名称为当前目录时退回视频文件名",
			build:    other,
			template: "{num}",
			fallback: ".",
			expected: "ABC-123",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sm := tt.build()
			result := sm.GenerateNameByTemplateOrKeep(tt.template, tt.fallback)
			if result != tt.expected {
				t.Errorf("模板 '%s' 生成失败\n期望: %s\n实际: %s", tt.template, tt.expected, result)
			}
		})
	}
}

func TestOldSyntax_ActorAliasAndNewSyntaxVars(t *testing.T) {
	sm := createTestMovieData()

	tests := []struct {
		name     string
		template string
		expected string
	}{
		{
			name:     "actor 与 actors 取值相同",
			template: "{title} - {actor}",
			expected: "星际穿越 - 马修·麦康纳, 安妮·海瑟薇",
		},
		{
			name:     "actors 和 actor 混用",
			template: "{actors}/{actor}",
			expected: "马修·麦康纳, 安妮·海瑟薇/马修·麦康纳, 安妮·海瑟薇",
		},
		{
			name:     "单花括号使用新语法的分辨率变量",
			template: "{title} [{videoFormat}] {edition}",
			expected: "星际穿越 [1080p] FHD",
		},
		{
			name:     "单花括号使用新语法的 ID 变量",
			template: "{title} {tmdbid} {imdbid}",
			expected: "星际穿越 157336 tt0816692",
		},
		{
			name:     "单花括号使用新语法的编码和时长变量",
			template: "{title} {videoCodec} {audioCodec} {runtime}min",
			expected: "星际穿越 h264 aac 169min",
		},
		{
			name:     "单花括号使用新语法的评分变量",
			template: "{title} - {vote_average}",
			expected: "星际穿越 - 8.600000",
		},
		{
			name:     "单花括号使用新语法的语言和扩展名变量",
			template: "{title}.{original_language}{fileExt}",
			expected: "星际穿越.en.mkv",
		},
		{
			name:     "tmdb_id 与 tmdbid 同时使用",
			template: "{title} {tmdb_id} {tmdbid}",
			expected: "星际穿越 {tmdbid-157336} 157336",
		},
		{
			name:     "未定义的变量原样保留",
			template: "{title}{unknown}",
			expected: "星际穿越{unknown}",
		},
		{
			name:     "当前媒体类型不支持的变量原样保留",
			template: "{title}{season_episode}",
			expected: "星际穿越{season_episode}",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := sm.GenerateNameByTemplate(tt.template)
			if result != tt.expected {
				t.Errorf("模板 '%s' 生成失败\n期望: %s\n实际: %s", tt.template, tt.expected, result)
			}
		})
	}
}

func TestOldSyntax_TVShowNewSyntaxVars(t *testing.T) {
	sm := createTestTVShowData()

	tests := []struct {
		name     string
		template string
		expected string
	}{
		{
			name:     "单花括号使用新语法的季集变量",
			template: "{title} S{season}E{episode}",
			expected: "猎魔人 S2E8",
		},
		{
			name:     "单花括号使用新语法的集标题变量",
			template: "{title} {season_episode} - {episode_title}",
			expected: "猎魔人 S02E08 - 穿越时空的界限",
		},
		{
			name:     "单花括号使用新语法的季年份变量",
			template: "{title} ({season_year})",
			expected: "猎魔人 (2020)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := sm.GenerateNameByTemplate(tt.template)
			if result != tt.expected {
				t.Errorf("模板 '%s' 生成失败\n期望: %s\n实际: %s", tt.template, tt.expected, result)
			}
		})
	}
}

func TestNewSyntax_ActorAlias(t *testing.T) {
	sm := createTestMovieData()

	tests := []struct {
		name     string
		template string
		expected string
	}{
		{
			name:     "actor 与 actors 取值相同",
			template: "{{title}} - {{actor}}",
			expected: "星际穿越 - 马修·麦康纳, 安妮·海瑟薇",
		},
		{
			name:     "actor 参与条件判断",
			template: "{{title}}{% if actor %} - {{actor}}{% endif %}",
			expected: "星际穿越 - 马修·麦康纳, 安妮·海瑟薇",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := sm.GenerateNameByTemplate(tt.template)
			if result != tt.expected {
				t.Errorf("模板 '%s' 生成失败\n期望: %s\n实际: %s", tt.template, tt.expected, result)
			}
		})
	}
}
