package scrape

import (
	"context"
	"errors"
	"fmt"

	"qmediasync/internal/baidupan"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
	"qmediasync/internal/openlist"
	"qmediasync/internal/scrape/rename"
	v115open "qmediasync/internal/v115/openapi"
)

// 重命名电影文件
type renameMovieImpl struct {
	scrapePath *models.ScrapePath
	ctx        context.Context
	renameImpl renameImpl
}

func NewRenameMovieImpl(scrapePath *models.ScrapePath, ctx context.Context, v115Client *v115open.OpenClient, openlistClient *openlist.Client, baiduPanClient *baidupan.Client) renameImpl {
	var ri renameImpl
	switch scrapePath.SourceType {
	case models.SourceType115:
		ri = rename.NewRename115(ctx, scrapePath, v115Client)
	case models.SourceTypeOpenList:
		ri = rename.NewRenameOpenList(ctx, scrapePath, openlistClient)
	case models.SourceTypeBaiduPan:
		ri = rename.NewRenameBaiduPan(ctx, scrapePath, baiduPanClient)
	default:
		ri = rename.NewRenameLocal(ctx, scrapePath)
	}
	return &renameMovieImpl{
		scrapePath: scrapePath,
		ctx:        ctx,
		renameImpl: ri,
	}
}

func (r *renameMovieImpl) RenameAndMove(mediaFile *models.ScrapeMediaFile, destPath, destPathId, newName string) error {
	if mediaFile.NewPathId == "" {
		helpers.AppLogger.Errorf("新目录 ID 为空，无法改名和移动文件")
		return errors.New("新目录 ID 为空")
	}
	// 先转移视频
	// 需要新目录 ID，然后将视频文件移动过去
	// 然后重命名成新名字
	if destPath == "" || destPathId == "" {
		destPath, destPathId = mediaFile.GetMovieOrTvshowDestPath()
	}
	if newName == "" {
		newName = fmt.Sprintf("%s%s", mediaFile.NewVideoBaseName, mediaFile.VideoExt)
	}
	helpers.AppLogger.Infof("开始根据整理规则重命名或移动文件：%s", newName)
	if err := r.renameImpl.RenameAndMove(mediaFile, destPath, destPathId, newName); err != nil {
		return err
	}
	return nil
}

func (r *renameMovieImpl) CheckAndMkDir(destFullPath, rootPath, rootPathId string) (string, error) {
	newPathId, err := r.renameImpl.CheckAndMkDir(destFullPath, rootPath, rootPathId)
	if err != nil {
		helpers.AppLogger.Errorf("创建父文件夹失败：%v", err)
		return "", err
	}
	return newPathId, nil
}

func (r *renameMovieImpl) RemoveMediaSourcePath(mediaFile *models.ScrapeMediaFile, sp *models.ScrapePath) error {
	return r.renameImpl.RemoveMediaSourcePath(mediaFile, sp)
}

func (r *renameMovieImpl) ReadFileContent(fileId string) ([]byte, error) {
	return r.renameImpl.ReadFileContent(fileId)
}

func (r *renameMovieImpl) CheckAndDeleteFiles(mediaFile *models.ScrapeMediaFile, files []models.WillDeleteFile) error {
	return r.renameImpl.CheckAndDeleteFiles(mediaFile, files)
}

func (r *renameMovieImpl) MoveFiles(f models.MoveNewFileToSourceFile) error {
	return r.renameImpl.MoveFiles(f)
}

func (r *renameMovieImpl) DeleteDir(path, pathId string) error {
	return r.renameImpl.DeleteDir(path, pathId)
}

func (r *renameMovieImpl) Rename(fileId, newName string) error {
	return r.renameImpl.Rename(fileId, newName)
}

func (r *renameMovieImpl) ExistsAndRename(fileId, newName string) (string, error) {
	return r.renameImpl.ExistsAndRename(fileId, newName)
}
