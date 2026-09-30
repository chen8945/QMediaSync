package scrape

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
)

// 识别电影

type IdMovieImpl struct {
	IdBase
}

func NewIdMovieImpl(scrapePath *models.ScrapePath, ctx context.Context, tmdbImpl TmdbImpl) *IdMovieImpl {
	return &IdMovieImpl{
		tmdbImpl:   tmdbImpl,
		scrapePath: scrapePath,
		ctx:        ctx,
	}
}

// 识别电影
func (i *IdMovieImpl) Identify(mediaFile *models.ScrapeMediaFile) error {
	// 如果已有 TMDB ID，则不继续识别；重新识别会自动填入 TMDB ID。
	if mediaFile.IsReScrape || mediaFile.TmdbId != 0 || i.scrapePath.MediaType == models.MediaTypeOther {
		return nil
	}
	// 从文件名和文件夹名中提取信息
	info, err := i.extractInfo(mediaFile)
	if info != nil && info.Name != "" {
		mediaFile.Name = helpers.CleanFileName(info.Name)
		mediaFile.Year = info.Year
		mediaFile.Save()
	}
	if err != nil {
		reason := err.Error()
		if err.Error() == "多条记录" {
			reason = "通过名称和年份查到多部电影，请手工重新识别并指定准确的 TMDB ID"
		}
		return fmt.Errorf("%s", reason)
	}
	if info.Name == "" || info.Year == 0 || info.TmdbId == 0 {
		return errors.New("无法从文件名和文件夹名中提取到完整的电影信息")
	}
	// 保存
	mediaFile.TmdbId = info.TmdbId
	mediaFile.Save()
	return nil
}

func (i *IdMovieImpl) extractInfo(mediaFile *models.ScrapeMediaFile) (*helpers.MediaInfo, error) {
	disableAI := false
	if i.scrapePath.EnableAi == models.AiActionEnforce {
		// 强制使用 AI，如果 AI 失败则退回正则识别
		disableAI = true
		aiInfo, err := i.extractInfoByAI(mediaFile)
		if err != nil {
			helpers.AppLogger.Errorf("使用 AI 提取电影信息失败，退回正则识别：%v", err)
		} else if aiInfo != nil {
			helpers.AppLogger.Infof("强制使用 AI 提取电影信息成功，文件名：%s，文件夹：%s，提取结果：%+v", mediaFile.VideoFilename, filepath.Base(mediaFile.Path), aiInfo)
			return &helpers.MediaInfo{
				Name:   aiInfo.Name,
				Year:   aiInfo.Year,
				TmdbId: aiInfo.TmdbId,
			}, nil
		}
	}
	info, err := i.extractInfoByRE(mediaFile)
	if err != nil {
		helpers.AppLogger.Errorf("使用正则提取电影信息失败：%v", err)
		if disableAI || i.scrapePath.EnableAi == models.AiActionOff {
			helpers.AppLogger.Errorf("由于禁用 AI 识别，所以直接返回错误：%v", err)
			return nil, err
		}
	} else if info.TmdbId != 0 {
		helpers.AppLogger.Infof("使用正则提取电影信息成功，文件名：%s，文件夹：%s，提取结果：%+v", mediaFile.VideoFilename, filepath.Base(mediaFile.Path), info)
		return &helpers.MediaInfo{
			Name:   info.Name,
			Year:   info.Year,
			TmdbId: info.TmdbId,
		}, nil
	}
	// 使用 AI 辅助查询
	assistAiInfo, assistErr := i.extractInfoByAI(mediaFile)
	if assistErr != nil {
		helpers.AppLogger.Errorf("使用 AI 辅助提取电影信息失败，退回正则识别：%v", assistErr)
		return nil, assistErr
	} else if assistAiInfo != nil {
		// helpers.AppLogger.Infof("使用 AI 辅助提取电影信息成功，文件名：%s，文件夹：%s，提取结果：%+v", mediaFile.VideoFilename, filepath.Base(mediaFile.Path), assistAiInfo)
		return &helpers.MediaInfo{
			Name:   assistAiInfo.Name,
			Year:   assistAiInfo.Year,
			TmdbId: assistAiInfo.TmdbId,
		}, nil
	}
	return nil, errors.New("使用 AI 辅助从文件名中提取媒体名称和年份失败，未找到匹配记录")
}

// AI 提取
func (i *IdMovieImpl) extractInfoByAI(mediaFile *models.ScrapeMediaFile) (*helpers.MediaInfo, error) {
	client := models.GlobalScrapeSettings.GetAiClient()
	info, err := client.TakeMoiveName(mediaFile.VideoFilename, i.scrapePath.GetAiPrompt())
	if err != nil {
		helpers.AppLogger.Errorf("强制使用 AI 从文件名中提取媒体信息失败：%v", err)
		return nil, err
	}
	if info.Name != "" && info.Year != 0 {
		// 查询
		name, checkId, cyear, cerr := i.tmdbImpl.CheckByNameAndYear(info.Name, info.Year, true)
		if cerr != nil {
			helpers.AppLogger.Errorf("AI 从文件名中查询名称和年份失败，文件名：%s，提取结果：%+v，错误信息：%v", mediaFile.VideoFilename, info, cerr)
		}
		if checkId > 0 {
			helpers.AppLogger.Infof("AI 从文件名中查询名称和年份成功，文件名：%s，提取结果：%+v，名称：%s，TMDB ID：%d", mediaFile.VideoFilename, info, name, checkId)
			// 查到了，直接用
			return &helpers.MediaInfo{
				Name:   name,
				Year:   cyear,
				TmdbId: checkId,
			}, nil
		}
	}
	folderName := filepath.Base(mediaFile.Path)
	// 从文件夹中提取信息
	helpers.AppLogger.Warnf("AI 从文件名中提取的媒体信息不完整，继续从文件夹中补齐，文件名：%s，提取结果：%+v", mediaFile.VideoFilename, info)
	folderInfo, err := client.TakeMoiveName(folderName, i.scrapePath.GetAiPrompt())
	if err != nil {
		helpers.AppLogger.Errorf("AI 从文件夹中提取媒体信息失败，文件夹：%s，错误信息：%v", folderName, err)
		return nil, err
	}
	helpers.AppLogger.Infof("AI 从文件夹中提取媒体信息，文件夹：%s，提取结果：%+v", folderName, folderInfo)
	if folderInfo.Year != 0 {
		info.Year = folderInfo.Year
	}
	if folderInfo.Name != "" && info.Name == "" {
		info.Name = folderInfo.Name
	}
	// 使用提取结果查询
	name, checkId, cyear, err := i.tmdbImpl.CheckByNameAndYear(info.Name, info.Year, true)
	if err != nil {
		helpers.AppLogger.Errorf("AI 从文件夹中提取媒体信息后查询名称和年份失败，文件夹：%s，提取结果：%+v，错误信息：%v", folderName, folderInfo, err)
		return nil, err
	}
	if checkId > 0 {
		helpers.AppLogger.Infof("AI 从文件夹中提取媒体信息后查询名称和年份成功，文件夹：%s，提取结果：%+v，名称：%s，TMDB ID：%d", folderName, folderInfo, name, checkId)
		// 查到了，直接用
		return &helpers.MediaInfo{
			Name:   name,
			Year:   cyear,
			TmdbId: checkId,
		}, nil
	}
	return nil, errors.New("AI 从文件夹中提取媒体信息后查询名称和年份失败，未找到匹配记录")
}

// 正则提取
func (i *IdMovieImpl) extractInfoByRE(mediaFile *models.ScrapeMediaFile) (*helpers.MediaInfo, error) {
	folderName := filepath.Base(mediaFile.Path)
	filename := filepath.Base(mediaFile.VideoFilename)
	// 从文件名中获取媒体信息
	info := helpers.ExtractMediaInfoRe(filename, true, false, i.scrapePath.VideoExtList, i.scrapePath.DeleteKeyword...)
	if info.TmdbId != 0 {
		// 使用 TMDB ID 查询
		cname, cyear, cerr := i.tmdbImpl.CheckByTmdbId(info.TmdbId)
		if cerr != nil {
			helpers.AppLogger.Errorf("使用 TMDB ID 查询媒体信息失败，TMDB ID：%d，错误信息：%v", info.TmdbId, cerr)
		} else {
			if cname != "" {
				info.Name = cname
			}
			if cyear != 0 {
				info.Year = cyear
			}
			return info, nil
		}
	}
	if info.Name != "" && info.Year != 0 {
		// 使用名称和年份查询
		cname, cid, cyear, cerr := i.tmdbImpl.CheckByNameAndYear(info.Name, info.Year, true)
		if cerr != nil {
			helpers.AppLogger.Errorf("使用名称和年份查询媒体信息失败，名称：%s，年份：%d，错误信息：%v", info.Name, info.Year, cerr)
		} else {
			info.TmdbId = cid
			info.Year = cyear
			info.Name = cname
			helpers.AppLogger.Infof("使用正则从文件名中提取媒体信息成功，文件名：%s，提取结果：%+v", filename, info)
			return info, nil
		}
	}
	helpers.AppLogger.Warnf("文件名 %s 缺少名称或年份，继续从文件夹中提取", filename)
	// 使用正则从文件夹中提取信息
	folderInfo := helpers.ExtractMediaInfoRe(folderName, true, false, i.scrapePath.VideoExtList, i.scrapePath.DeleteKeyword...)
	helpers.AppLogger.Errorf("正则从文件夹中提取信息，文件夹：%s，提取结果：%+v", folderName, folderInfo)
	if folderInfo.TmdbId != 0 {
		info.TmdbId = folderInfo.TmdbId
	}
	if folderInfo.Name != "" {
		info.Name = folderInfo.Name
	}
	if folderInfo.Year != 0 {
		info.Year = folderInfo.Year
	}
	// 从 TMDB 查询
	if info.TmdbId != 0 {
		// 使用 TMDB ID 查询
		cname, cyear, cerr := i.tmdbImpl.CheckByTmdbId(info.TmdbId)
		if cerr != nil {
			helpers.AppLogger.Errorf("使用 TMDB ID 查询媒体信息失败，TMDB ID：%d，错误信息：%v", info.TmdbId, cerr)
		} else {
			info.Name = cname
			info.Year = cyear
			helpers.AppLogger.Infof("使用 TMDB ID 查询媒体信息成功，TMDB ID：%d，名称：%s，年份：%d", info.TmdbId, info.Name, info.Year)
			return info, nil
		}
	}
	if info.Name != "" {
		// 使用名称和年份查询
		cname, cid, cyear, cerr := i.tmdbImpl.CheckByNameAndYear(info.Name, info.Year, true)
		if cerr != nil {
			helpers.AppLogger.Errorf("使用名称和年份查询媒体信息失败，名称：%s，年份：%d，错误信息：%v", info.Name, info.Year, cerr)
			return nil, cerr
		} else {
			info.TmdbId = cid
			info.Year = cyear
			info.Name = cname
			helpers.AppLogger.Infof("使用名称和年份查询媒体信息成功，名称：%s，年份：%d，TMDB ID：%d", info.Name, info.Year, info.TmdbId)
			return info, nil
		}
	}
	return nil, fmt.Errorf("文件名 %s 无法提取到任何媒体信息", filename)
}
