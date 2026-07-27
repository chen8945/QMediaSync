package v115open

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"qmediasync/internal/helpers"
)

type DownloadUrlData struct {
	FileName string      `json:"file_name"`
	FileSize json.Number `json:"file_size"`
	PickCode string      `json:"pick_code"`
	Sha1     string      `json:"sha1"`
	Url      struct {
		Url string `json:"url"`
	} `json:"url"`
}

type DownloadUrlResp struct {
	RespBaseBool[map[string]DownloadUrlData]
}

// DownloadUrlResult 是 115 下载地址接口返回、可供队列持久化的非敏感文件信息。
type DownloadUrlResult struct {
	URL      string
	FileName string
	PickCode string
	Sha1     string
}

// ├─file_id	string	文件 ID
// ├─parent_id	string	文件父目录 ID
// ├─file_name	string	文件名称
// ├─file_size	string	文件大小
// ├─file_sha1	string	文件哈希值
// ├─file_type	string	文件类型
// ├─is_private	string	文件是否加密隐藏；0：否；1：是
// ├─play_long	string	视频文件时长
// ├─ user_def	int	视频文件记忆选中的清晰度；1:标清 2:高清 3:超清 4:1080P 5:4K;100:原画
// ├─ user_rotate	int	记忆视频旋转角度；0, 90, 180, 270
// ├─ user_turn	int	视频翻转方向：0：不翻转；1：水平翻转；2：垂直翻转
// ├─ multitrack_list	array	视频多音轨列表
//
//	├─ title	string	音轨标题
//	├─ is_selected	string	音轨是否上次选中；1：选中
//
// ├─ definition_list_new	array	视频可切换清晰度列表；1:标清 2:高清 3:超清 4:1080P 5:4K;100:原画
// ├─ video_url	array	视频各清晰度的播放地址信息
//
//	├─ url	string	播放地址
//	├─ height	int	视频高度
//	├─ width	int	视频宽度
//	├─ definition	int	视频清晰度
//	├─ title	int	视频清晰度名称
//	├─ definition_n	int	视频清晰度（新）
type VideoPlayUrlData struct {
	FileId         string `json:"file_id"`
	ParentId       string `json:"parent_id"`
	FileName       string `json:"file_name"`
	FileSize       string `json:"file_size"`
	FileSha1       string `json:"file_sha1"`
	FileType       string `json:"file_type"`
	IsPrivate      string `json:"is_private"`
	PlayLong       string `json:"play_long"`
	UserDef        int    `json:"user_def"`
	UserRotate     int    `json:"user_rotate"`
	UserTurn       int    `json:"user_turn"`
	MultitrackList []struct {
		Title      string `json:"title"`
		IsSelected string `json:"is_selected"`
	} `json:"multitrack_list"`
	DefinitionListNew map[string]string `json:"definition_list_new"` // 清晰度列表{
	VideoUrl          []struct {
		Url         string `json:"url"`
		Height      int    `json:"height"`
		Width       int    `json:"width"`
		Definition  int    `json:"definition"`
		Title       string `json:"title"`
		DefinitionN int    `json:"definition_n"`
	} `json:"video_url"`
}

// ├─ endpoint	string	上传域名
// ├─ AccessKeySecret	string	上传凭证-密钥
// ├─ SecurityToken	string	上传凭证 Token
// ├─ Expiration	string	上传凭证-过期日期
// ├─ AccessKeyId	string	上传凭证 ID

type UploadToken struct {
	Endpoint         string `json:"endpoint"`
	AccessKeySecret  string `json:"AccessKeySecret"`
	AccessKeySecrett string `json:"AccessKeySecrett"`
	SecurityToken    string `json:"SecurityToken"`
	Expiration       string `json:"Expiration"`
	AccessKeyId      string `json:"AccessKeyId"`
}

func (token *UploadToken) normalize() {
	if token.AccessKeySecret == "" {
		token.AccessKeySecret = token.AccessKeySecrett
	}
}

type UploadResultCallBack struct {
	Callback    string `json:"callback"`
	CallbackVar string `json:"callback_var"`
}
type UploadResult[T any] struct {
	PickCode  string `json:"pick_code"`
	Status    int    `json:"status"`
	FileId    string `json:"file_id"`
	Target    string `json:"target"`
	Bucket    string `json:"bucket"`
	Object    string `json:"object"`
	SignKey   string `json:"sign_key"`
	SignCheck string `json:"sign_check"`
	Callback  T      `json:"callback"`
}

// UploadMultipartInput 是 OpenClient 发起 OSS multipart 上传的输入。
type UploadMultipartInput struct {
	Bucket      string
	Object      string
	Callback    string
	CallbackVar string
	FilePath    string
	FileSize    int64
	UploadId    string
	PartSize    int64
	OnProgress  func(OSSMultipartProgress)
}

// 获取文件下载地址
// POST 域名 + /open/ufile/downurl
func (c *OpenClient) GetDownloadUrl(ctx context.Context, pickCode string, userAgent string, bypassRateLimit bool) string {
	result := c.GetDownloadUrlResult(ctx, pickCode, userAgent, bypassRateLimit)
	if result == nil {
		return ""
	}
	return result.URL
}

// GetDownloadUrlResult 获取下载地址及 115 明确返回的文件身份信息。
func (c *OpenClient) GetDownloadUrlResult(ctx context.Context, pickCode string, userAgent string, bypassRateLimit bool) *DownloadUrlResult {
	params := map[string]string{
		"pick_code": pickCode,
	}
	url := fmt.Sprintf("%s/open/ufile/downurl", OPEN_BASE_URL)
	req := c.client.R().SetFormData(params).SetMethod("POST").SetHeader("User-Agent", userAgent)
	respData := DownloadUrlResp{}
	config := MakeRequestConfig(0, 0, 0)
	config.BypassRateLimit = bypassRateLimit
	_, respBytes, err := c.doAuthRequest(ctx, url, req, config, nil)
	if err != nil {
		helpers.V115Log.Errorf("获取文件下载地址失败：%v", err)
		return nil
	}
	jsonErr := json.Unmarshal(respBytes, &respData)
	if jsonErr != nil || !respData.State {
		helpers.V115Log.Errorf("获取文件下载地址失败：%v", jsonErr)
		return nil
	}
	data := respData.Data
	var first DownloadUrlData
	for _, v := range data {
		first = v
		break
	}
	if first.Url.Url == "" {
		return nil
	}
	return &DownloadUrlResult{
		URL:      first.Url.Url,
		FileName: first.FileName,
		PickCode: first.PickCode,
		Sha1:     first.Sha1,
	}
}

// 获取视频播放链接
// POST 域名 + /open/ufile/downurl
func (c *OpenClient) GetVideoPlayUrl(ctx context.Context, pickCode string, userAgent string) *VideoPlayUrlData {
	params := map[string]string{
		"pick_code": pickCode,
	}
	url := fmt.Sprintf("%s/open/video/play", OPEN_BASE_URL)
	req := c.client.R().SetQueryParams(params).SetMethod("GET").SetHeader("User-Agent", userAgent)
	respData := &VideoPlayUrlData{}
	_, _, err := c.doAuthRequest(ctx, url, req, MakeRequestConfig(0, 0, 0), respData)
	if err != nil {
		helpers.V115Log.Errorf("获取视频播放地址失败：%v", err)
		return nil
	}
	return respData
}

// 初始化上传进程
// POST 域名 + /open/upload/init
func (c *OpenClient) Upload(ctx context.Context, filePath string, parentFileId string, signKey string, signVal string) (string, error) {
	fileInfo, err := os.Stat(filePath)
	if err != nil {
		helpers.V115Log.Errorf("获取文件信息失败：%v", err)
		return "", err
	}
	fileName := fileInfo.Name()
	fileSize := fileInfo.Size()
	fileSha1, err := helpers.FileSHA1(filePath)
	if err != nil {
		helpers.V115Log.Errorf("计算文件 SHA1 失败：%v", err)
		return "", err
	}
	preSha1, err := helpers.FileSHA1Partial(filePath, 0, 128*1024-1)
	if err != nil {
		helpers.V115Log.Errorf("计算文件前 128 KiB SHA1 失败：%v", err)
		return "", err
	}
	initRequest := UploadInitRequest{
		FileName:     fileName,
		FileSize:     fileSize,
		ParentFileId: parentFileId,
		FileSha1:     fileSha1,
		Preid:        preSha1,
		TopUpload:    "0",
		SignKey:      signKey,
		SignVal:      signVal,
	}
	helpers.V115Log.Infof("准备上传文件：%s，大小：%d，SHA1：%s，前 128 KiB SHA1：%s，Parent ID：%s", fileName, fileSize, fileSha1, preSha1, parentFileId)
	initResult, err := c.UploadInit(ctx, initRequest)
	if err != nil {
		helpers.V115Log.Errorf("上传初始化失败：%v", err)
		return "", err
	}
	status := initResult.Status
	if status == 7 {
		signVal, err := signValueForRange(filePath, initResult.SignCheck)
		if err != nil {
			return "", err
		}
		initRequest.SignKey = initResult.SignKey
		initRequest.SignVal = signVal
		initResult, err = c.UploadInit(ctx, initRequest)
		if err != nil {
			return "", err
		}
		status = initResult.Status
	}
	if status == 2 {
		// 秒传成功
		return initResult.FileId, nil
	}
	if status == 6 {
		helpers.V115Log.Error("签名验证后失败")
		return "", fmt.Errorf("签名验证后失败")
	}
	if status == 8 {
		helpers.V115Log.Error("签名认证失败")
		return "", fmt.Errorf("签名认证失败")
	}
	if status == 1 {
		// 非秒传，开始普通上传流程
		// 获取上传凭证
		uploadToken := c.GetUploadToken(ctx)
		if uploadToken == nil {
			helpers.V115Log.Error("获取上传凭证失败")
			return "", fmt.Errorf("获取上传凭证失败")
		}
		callback := initResult.Callback.Callback
		callbackVar := initResult.Callback.CallbackVar
		bucket := initResult.Bucket
		objectId := initResult.Object
		helpers.V115Log.Infof("准备 OSS multipart 上传：bucket=%s，object_id=%s，endpoint=%s，AccessKeyId=%s", bucket, objectId, uploadToken.Endpoint, uploadToken.AccessKeyId)
		uploader := NewOSSMultipartUploader(uploadToken.Endpoint, uploadToken.AccessKeyId, uploadToken.AccessKeySecret, uploadToken.SecurityToken)
		callbackResult, ossErr := uploader.UploadFile(ctx, OSSMultipartUploadInput{
			Bucket:      bucket,
			Object:      objectId,
			Callback:    callback,
			CallbackVar: callbackVar,
			FilePath:    filePath,
			FileSize:    fileSize,
			refreshClient: func(ctx context.Context) (ossMultipartClient, error) {
				refreshedToken := c.GetUploadToken(ctx)
				return newOSSMultipartClientFromToken(refreshedToken)
			},
		})
		if ossErr != nil {
			return "", ossErr
		}
		completeResult, err := ParseCompleteCallbackResult(callbackResult)
		if err != nil {
			return "", err
		}

		return completeResult.FileId, nil
	}
	return initResult.FileId, nil
}

// 获取 115 上传凭证
// GET /open/upload/get_token
func (c *OpenClient) GetUploadToken(ctx context.Context) *UploadToken {
	url := fmt.Sprintf("%s/open/upload/get_token", OPEN_BASE_URL)
	req := c.client.R().SetMethod("GET")
	respData := &UploadToken{}
	_, _, uErr := c.doAuthRequest(ctx, url, req, MakeRequestConfig(0, 0, 0), respData)
	if uErr != nil {
		helpers.V115Log.Errorf("获取上传凭证失败：%v", uErr)
		return nil
	}
	respData.normalize()
	return respData
}

// UploadMultipartWithResult 使用 115 临时凭证上传 OSS multipart，并在 part 失败重试前刷新凭证。
func (c *OpenClient) UploadMultipartWithResult(ctx context.Context, input UploadMultipartInput) (OSSMultipartUploadResult, error) {
	uploadToken := c.GetUploadToken(ctx)
	if uploadToken == nil {
		return OSSMultipartUploadResult{}, fmt.Errorf("获取上传凭证失败")
	}
	helpers.V115Log.Infof(
		"准备 OSS multipart 上传：bucket=%s，object_id=%s，endpoint=%s，AccessKeyId=%s",
		input.Bucket,
		input.Object,
		uploadToken.Endpoint,
		uploadToken.AccessKeyId,
	)
	uploader := NewOSSMultipartUploader(
		uploadToken.Endpoint,
		uploadToken.AccessKeyId,
		uploadToken.AccessKeySecret,
		uploadToken.SecurityToken,
	)
	return uploader.UploadFileWithResult(ctx, OSSMultipartUploadInput{
		Bucket:      input.Bucket,
		Object:      input.Object,
		Callback:    input.Callback,
		CallbackVar: input.CallbackVar,
		FilePath:    input.FilePath,
		FileSize:    input.FileSize,
		UploadId:    input.UploadId,
		PartSize:    input.PartSize,
		OnProgress:  input.OnProgress,
		refreshClient: func(ctx context.Context) (ossMultipartClient, error) {
			refreshedToken := c.GetUploadToken(ctx)
			return newOSSMultipartClientFromToken(refreshedToken)
		},
	})
}

// 上传分片
func (c *OpenClient) UploadResume(ctx context.Context, pickCode string, fileSize int64, parentFileId string, fileSha1 string) (*UploadResumeResult, error) {
	params := map[string]string{
		"file_size": fmt.Sprintf("%d", fileSize),
		"target":    fmt.Sprintf("U_1_%s", parentFileId),
		"fileid":    fileSha1,
		"pick_code": pickCode,
	}
	url := fmt.Sprintf("%s/open/upload/resume", OPEN_BASE_URL)
	req := c.client.R().SetFormData(params).SetMethod("POST")
	respData := &uploadScheduleAPIResult{}
	if _, _, err := c.doAuthRequest(ctx, url, req, MakeRequestConfig(1, 1, 15), respData); err != nil {
		return nil, err
	}
	return respData.toUploadResumeResult()
}
