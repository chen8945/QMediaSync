package baidupan

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"qmediasync/internal/helpers"
	openapiclient "qmediasync/openxpanapi"
)

type Client struct {
	client      *openapiclient.APIClient
	accessToken string
}

type FileListOptions struct {
	Order string
	Desc  *int32
}

// 解析响应
type RefreshResponse struct {
	AccessToken  string `json:"access_token"`
	ExpiresIn    int64  `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
}

// 全局 HTTP 客户端实例
var cachedClients map[string]*Client = make(map[string]*Client, 0)
var cachedClientsMutex sync.RWMutex

func NewBaiDuPanClient(accountId uint, accessToken string) *Client {
	cachedClientsMutex.Lock()
	defer cachedClientsMutex.Unlock()
	clientKey := fmt.Sprintf("%d", accountId)
	if client, exists := cachedClients[clientKey]; exists {
		client.accessToken = accessToken
		return client
	}
	client := newBaiDuPanClient(accessToken)
	cachedClients[clientKey] = client
	return client
}

// NewBaiDuPanClientWithToken 创建不进入共享缓存的客户端，用于在授权凭据写入账号前验证新授权。
func NewBaiDuPanClientWithToken(accessToken string) *Client {
	return newBaiDuPanClient(accessToken)
}

func newBaiDuPanClient(accessToken string) *Client {
	config := openapiclient.NewConfiguration()
	// if !helpers.IsRelease {
	// 	config.Debug = true
	// }
	apiClient := openapiclient.NewAPIClient(config)
	client := &Client{
		client:      apiClient,
		accessToken: accessToken,
	}
	return client
}

func RefreshToken(accountId uint, refreshToken string) (*RefreshResponse, error) {
	// 生成 state 参数
	type stateData struct {
		Time         int64  `json:"time"`
		RefreshToken string `json:"refresh_token"`
	}
	stateObj := stateData{
		Time:         time.Now().Unix(),
		RefreshToken: refreshToken,
	}
	stateJson, _ := json.Marshal(stateObj)
	stateEncoded, err := helpers.Encrypt(string(stateJson))
	if err != nil {
		return nil, err
	}
	// 构建授权 URL
	authServerUrl := fmt.Sprintf("%s/baidupan/oauth-url", helpers.GlobalConfig.AuthServer)
	// 注意：redirect_uri 需要与百度开放平台配置一致
	oauthUrl := fmt.Sprintf("%s?action=refresh&state=%s", authServerUrl, stateEncoded)
	// 发送 GET 请求
	resp, err := http.Get(oauthUrl)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var refreshResp RefreshResponse
	err = json.Unmarshal(body, &refreshResp)
	if err != nil {
		return nil, err
	}
	// 设置新的访问凭证
	return &refreshResp, nil
}

func UpdateToken(accountId uint, accessToken string) {
	cachedClientsMutex.Lock()
	defer cachedClientsMutex.Unlock()
	clientKey := fmt.Sprintf("%d", accountId)
	if client, exists := cachedClients[clientKey]; exists {
		client.accessToken = accessToken
	}
}

// UpdateTokenIfCurrent 仅当缓存客户端仍持有预期令牌时更新 access token。
func UpdateTokenIfCurrent(accountId uint, expectedAccessToken string, accessToken string) bool {
	cachedClientsMutex.Lock()
	defer cachedClientsMutex.Unlock()
	clientKey := fmt.Sprintf("%d", accountId)
	client, exists := cachedClients[clientKey]
	if !exists || client.accessToken != expectedAccessToken {
		return false
	}
	client.accessToken = accessToken
	return true
}

func (c *Client) SetAuthToken(accessToken string) {
	c.accessToken = accessToken
}

// 统一处理错误
func (c *Client) handleError(err error, resp *http.Response, respData any) error {
	if err != nil {
		return err
	}
	// 读取 body 并解码 JSON
	defer resp.Body.Close()
	body, ioErr := io.ReadAll(resp.Body)
	if ioErr != nil {
		return ioErr
	}
	// 记录日志
	helpers.BaiduPanLog.Infof("百度 SDK 请求响应：%s %s\n%s", resp.Request.Method, resp.Request.URL, string(body))
	// 解码 JSON
	type ErrorResponse struct {
		Errmsg string `json:"errmsg"`
		Errno  int64  `json:"errno"`
	}
	var respBody ErrorResponse
	err = json.Unmarshal(body, &respBody)
	if err != nil {
		return err
	}
	// 检查 errno 是否为 0
	if respBody.Errno != 0 {
		msg := respBody.Errmsg
		if msg == "" {
			var ok bool
			msg, ok = ErrorMap[respBody.Errno]
			if !ok {
				msg = fmt.Sprintf("未知错误码 %d", respBody.Errno)
			}
		}
		return fmt.Errorf("百度 SDK 请求失败：%s", msg)
	}
	// 检查 respData 是否为空
	if respData == nil {
		helpers.BaiduPanLog.Errorf("百度 SDK 请求失败：响应数据为空")
		return err
	}
	return nil
}

func (c *Client) GetUserInfo(ctx context.Context) (*openapiclient.Uinforesponse, error) {
	resp, r, err := c.client.UserinfoApi.Xpannasuinfo(ctx).AccessToken(c.accessToken).Execute()
	// 统一处理错误
	if c.handleError(err, r, resp) != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Client) GetQuota(ctx context.Context) (*openapiclient.Quotaresponse, error) {
	resp, r, err := c.client.UserinfoApi.Apiquota(ctx).AccessToken(c.accessToken).Execute()
	// 统一处理错误
	if c.handleError(err, r, resp) != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Client) GetFileList(ctx context.Context, parentPath string, onlyDir int, showEmby int32, start int32, limit int32) ([]*FileInfo, error) {
	return c.GetFileListWithOptions(ctx, parentPath, onlyDir, showEmby, start, limit, FileListOptions{})
}

func (c *Client) GetFileListWithOptions(ctx context.Context, parentPath string, onlyDir int, showEmby int32, start int32, limit int32, options FileListOptions) ([]*FileInfo, error) {
	startStr := fmt.Sprintf("%d", start)
	onlyDirStr := fmt.Sprintf("%d", onlyDir)
	if parentPath == "" {
		parentPath = "/"
	}
	// 如果 parentPath 不以 / 开头，则加上 /。
	if !strings.HasPrefix(parentPath, "/") {
		parentPath = "/" + parentPath
	}
	// 将所有\转为/
	parentPath = filepath.ToSlash(parentPath)
	req := c.client.FileinfoApi.Xpanfilelist(ctx).
		AccessToken(c.accessToken).
		Web("1").
		Dir(parentPath).
		Folder(onlyDirStr).
		Showempty(showEmby).
		Start(startStr).
		Limit(limit)
	if options.Order != "" {
		req = req.Order(options.Order)
	}
	if options.Desc != nil {
		req = req.Desc(*options.Desc)
	}
	resp, r, err := req.Execute()
	// 统一处理错误
	if herr := c.handleError(err, r, resp); herr != nil {
		helpers.AppLogger.Warnf("获取百度网盘目录列表失败，目录：%s，错误：%v", parentPath, herr)
		return nil, herr
	}
	// 记录日志
	// 解码 resp
	var fileList *FileListResponse
	err = json.Unmarshal([]byte(resp), &fileList)
	if err != nil {
		return nil, err
	}
	return fileList.List, nil
}

// 调用递归接口获取所有文件，必须传入文件修改时间（上一次同步时间）
func (c *Client) GetAllFiles(ctx context.Context, parentPath string, start int, limit int, mtime int64) (*FileListAllResponse, error) {
	helpers.AppLogger.Infof("递归获取百度网盘文件列表：父目录：%s，开始：%d，限制：%d，修改时间：%s", parentPath, start, limit, helpers.FormatUnixLogTime(mtime))
	if parentPath == "" {
		parentPath = "/"
	}
	// 如果 parentPath 不以 / 开头，则加上 /。
	if !strings.HasPrefix(parentPath, "/") {
		parentPath = "/" + parentPath
	}
	// 将所有\转为/
	parentPath = filepath.ToSlash(parentPath)
	req := c.client.MultimediafileApi.Xpanfilelistall(ctx).AccessToken(c.accessToken).Recursion(int32(1)).Path(parentPath).Start(int32(start)).Limit(int32(limit))
	if mtime > 0 {
		req = req.Mtime(fmt.Sprintf("%d", mtime))
	}
	resp, r, err := req.Execute()
	// 统一处理错误
	if c.handleError(err, r, resp) != nil {
		return nil, err
	}
	// 记录日志
	// 解码 resp
	var fileList *FileListAllResponse
	err = json.Unmarshal([]byte(resp), &fileList)
	if err != nil {
		return nil, err
	}
	return fileList, nil
}

// 查询文件详情，也能返回下载链接
func (c *Client) GetFileDetail(ctx context.Context, fileId string, dlink int32) (*FileDetail, error) {
	fsidsArr := []int64{}
	fsidsArr = append(fsidsArr, helpers.StringToInt64(fileId))
	// 转 JSON
	fsidsJson, err := json.Marshal(fsidsArr)
	if err != nil {
		return nil, err
	}
	fsids := string(fsidsJson)
	helpers.AppLogger.Infof("查询百度网盘文件详情：文件 ID：%s，获取下载链接：%d", fsidsJson, dlink)
	req := c.client.MultimediafileApi.Xpanmultimediafilemetas(ctx).AccessToken(c.accessToken).Fsids(fsids)
	if dlink == 1 {
		req = req.Dlink("1")
	}
	resp, r, err := req.Execute()
	// 统一处理错误
	if err := c.handleError(err, r, resp); err != nil {
		return nil, err
	}
	// 解码 resp
	var fileDetail FileDetailResponse
	err = json.Unmarshal([]byte(resp), &fileDetail)
	if err != nil {
		return nil, err
	}
	if len(fileDetail.List) == 0 || fileDetail.List[0] == nil {
		return nil, fmt.Errorf("百度网盘文件详情为空")
	}
	return fileDetail.List[0], nil
}

func (c *Client) Mkdir(ctx context.Context, path string) error {
	if path == "" {
		return fmt.Errorf("路径不能为空")
	}
	// 如果 path 不以 / 开头，则加上 /。
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	// 将所有\转为/
	path = filepath.ToSlash(path)
	resp, r, err := c.client.FileuploadApi.Xpanfilecreate(ctx).AccessToken(c.accessToken).Isdir(1).Path(path).Rtype(0).Execute()
	// 统一处理错误
	return c.handleError(err, r, resp)
}

func (c *Client) Del(ctx context.Context, pathes []string) error {
	// 将 pathes 转为 JSON
	newPathes := make([]string, 0)
	for _, path := range pathes {
		if !strings.HasPrefix(path, "/") {
			path = "/" + path
		}
		// 将所有\转为/
		path = filepath.ToSlash(path)
		newPathes = append(newPathes, path)
	}
	fileList, err := json.Marshal(newPathes)
	if err != nil {
		return err
	}
	fileListStr := string(fileList)
	r, err := c.client.FilemanagerApi.Filemanagerdelete(ctx).AccessToken(c.accessToken).Async(0).Filelist(fileListStr).Execute()
	// 统一处理错误
	return c.handleError(err, r, nil)
}

func CalculateChunkSizeByFileSize(fileSize int64) int64 {
	// <= 4 GB，4 MB 分片
	if fileSize <= 4*1024*1024*1024 {
		return 4 * 1024 * 1024
	}
	// <= 10 GB，16 MB 分片
	if fileSize <= 10*1024*1024*1024 {
		return 16 * 1024 * 1024
	}
	// <= 20 GB，32 MB 分片
	if fileSize <= 20*1024*1024*1024 {
		return 32 * 1024 * 1024
	}
	return 0
}

// 预上传
func (c *Client) PreCreate(ctx context.Context, localPath string, remotePath string) (*openapiclient.Fileprecreateresponse, *helpers.FileChunkMD5Result, error) {
	stat, err := os.Stat(localPath)
	if err != nil {
		return nil, nil, fmt.Errorf("本地文件不存在")
	}
	size := stat.Size()
	chunkSize := CalculateChunkSizeByFileSize(size)
	if chunkSize <= 0 {
		return nil, nil, fmt.Errorf("文件大小超出最大支持范围")
	}
	// 计算文件的分片
	chunkMD5, err := helpers.CalculateFileChunkMD5(localPath, chunkSize)
	if err != nil {
		return nil, nil, fmt.Errorf("计算文件分片 MD5 失败")
	}
	// 将 ChunkMD5.ChunkMD5s 转为 JSON
	chunkMD5sJson, err := json.Marshal(chunkMD5.ChunkMD5s)
	if err != nil {
		return nil, nil, fmt.Errorf("将 ChunkMD5.ChunkMD5s 转为 JSON 失败")
	}
	chunkMD5s := string(chunkMD5sJson)
	chunkMD5.ChunkMD5sJsonStr = chunkMD5s
	req := c.client.FileuploadApi.Xpanfileprecreate(ctx).AccessToken(c.accessToken).Path(remotePath).Size(int32(size)).Isdir(0).Autoinit(1).Rtype(2).BlockList(chunkMD5s)
	resp, r, err := req.Execute()
	// 统一处理错误
	if c.handleError(err, r, resp) != nil {
		return nil, nil, err
	}
	return &resp, chunkMD5, nil
}

// 上传文件
func (c *Client) Upload(ctx context.Context, localPath string, remotePath string) (*openapiclient.Filecreateresponse, error) {
	// 预上传
	preResp, chunkMD5, err := c.PreCreate(ctx, localPath, remotePath)
	if err != nil {
		return nil, fmt.Errorf("预上传失败：%w", err)
	}
	for _, seqNum := range *preResp.BlockList {
		// 提取文件分片
		tempFilePath, err := helpers.ExtractFileChunkToTemp(localPath, chunkMD5.ChunkSize, seqNum)
		if err != nil {
			return nil, fmt.Errorf("提取文件分片 %d 失败：%w", seqNum, err)
		}
		file, err := os.Open(tempFilePath)
		if err != nil {
			os.Remove(tempFilePath)
			return nil, fmt.Errorf("打开临时文件失败：%w", err)
		}
		// 上传分片
		uresp, ur, uerr := c.client.FileuploadApi.Pcssuperfile2(context.Background()).AccessToken(c.accessToken).Partseq(fmt.Sprintf("%d", seqNum)).Path(remotePath).Uploadid(*preResp.Uploadid).Type_("tmpfile").File(file).Execute()
		if c.handleError(uerr, ur, uresp) != nil {
			// 关闭且删除分片
			file.Close()
			os.Remove(tempFilePath)
			return nil, fmt.Errorf("上传文件分片 %d 失败：%w", seqNum, uerr)
		}
		// 关闭且删除分片
		file.Close()
		os.Remove(tempFilePath)
	}
	// 创建文件
	resp, r, err := c.client.FileuploadApi.Xpanfilecreate(ctx).AccessToken(c.accessToken).Path(remotePath).Isdir(0).Size(int32(chunkMD5.FileSize)).Uploadid(*preResp.Uploadid).BlockList(chunkMD5.ChunkMD5sJsonStr).Rtype(2).Execute()
	// 统一处理错误
	if c.handleError(err, r, resp) != nil {
		return nil, fmt.Errorf("创建文件失败：%w", err)
	}
	return &resp, nil
}

// 路径是否存在
func (c *Client) PathExists(ctx context.Context, path string) (bool, error) {
	if path == "" {
		return false, fmt.Errorf("路径不能为空")
	}
	_, err := c.GetFileList(ctx, path, 0, 1, 0, 1)
	if err != nil {
		return false, err
	}
	return true, nil
}

// 查询文件是否存在
func (c *Client) FileExists(ctx context.Context, path string) (*FileInfo, error) {
	if path == "" {
		return nil, fmt.Errorf("路径不能为空")
	}
	// 查询文件所在文件夹的列表，看文件是否存在
	parentPath := filepath.ToSlash(filepath.Dir(path))
	fileName := filepath.Base(path)
	start := 0
	for {
		fsList, err := c.GetFileList(ctx, parentPath, 0, 1, int32(start), 1000)
		if err != nil {
			return nil, err
		}
		for _, file := range fsList {
			if file.ServerFilename == fileName {
				return file, nil
			}
		}
		if len(fsList) < 1000 {
			break
		}
		start += 1000
	}
	return nil, nil
}

type ReNameItem struct {
	Path    string `json:"path"`
	NewName string `json:"newname"`
}

type MoveOrCopyItem struct {
	Path    string `json:"path"`
	Dest    string `json:"dest"`
	NewName string `json:"newname"`
}

// 重命名
// path：包含文件名的完整路径
// newName：新文件名
func (c *Client) Rename(ctx context.Context, path string, newName string) error {
	if path == "" {
		return fmt.Errorf("路径不能为空")
	}
	if newName == "" {
		return fmt.Errorf("旧文件名不能为空")
	}
	// 提取旧名字
	oldName := filepath.Base(path)
	if oldName == newName {
		return nil
	}
	// 构建请求参数
	fileList := []ReNameItem{
		{
			Path:    path,
			NewName: newName,
		},
	}
	fileListStr, err := json.Marshal(fileList)
	if err != nil {
		return fmt.Errorf("将 fileList 转为 JSON 失败：%w", err)
	}

	r, err := c.client.FilemanagerApi.Filemanagerrename(ctx).AccessToken(c.accessToken).Async(0).Ondup("skip").Filelist(string(fileListStr)).Execute()
	// 统一处理错误
	return c.handleError(err, r, nil)
}

// 批量重命名
func (c *Client) RenameBatch(ctx context.Context, fileList []ReNameItem) error {
	if len(fileList) == 0 {
		return fmt.Errorf("文件列表不能为空")
	}
	fileListStr, err := json.Marshal(fileList)
	if err != nil {
		return fmt.Errorf("将 fileList 转为 JSON 失败：%w", err)
	}

	r, err := c.client.FilemanagerApi.Filemanagerrename(ctx).AccessToken(c.accessToken).Async(0).Ondup("skip").Filelist(string(fileListStr)).Execute()
	// 统一处理错误
	return c.handleError(err, r, nil)
}

// 移动
// 如果 newName 不等于旧文件名，则会移动并改名
// path：包含文件名的完整路径
// newPath：新路径，不包含文件名
func (c *Client) Move(ctx context.Context, path string, newPath string, newName string) error {
	if path == "" {
		return fmt.Errorf("路径不能为空")
	}
	if newName == "" {
		return fmt.Errorf("旧文件名不能为空")
	}
	// 提取旧名字
	oldName := filepath.Base(path)
	if oldName == newName {
		return nil
	}
	// 构建请求参数
	fileList := []MoveOrCopyItem{
		{
			Path:    path,
			Dest:    newPath,
			NewName: newName,
		},
	}
	fileListStr, err := json.Marshal(fileList)
	if err != nil {
		return fmt.Errorf("将 fileList 转为 JSON 失败：%w", err)
	}

	resp, rerr := c.client.FilemanagerApi.Filemanagermove(ctx).AccessToken(c.accessToken).Async(0).Ondup("skip").Filelist(string(fileListStr)).Execute()
	return c.handleError(rerr, resp, nil)
}

// 批量移动
func (c *Client) MoveBatch(ctx context.Context, fileList []MoveOrCopyItem) error {
	if len(fileList) == 0 {
		return fmt.Errorf("文件列表不能为空")
	}
	newFileList := make([]MoveOrCopyItem, 0)
	// 检查 fileList，如果 path 和 dest 不以 / 开头，则在开头添加 /。
	for _, item := range fileList {
		if !strings.HasPrefix(item.Path, "/") {
			item.Path = "/" + item.Path
		}
		if !strings.HasPrefix(item.Dest, "/") {
			item.Dest = "/" + item.Dest
		}
		newFileList = append(newFileList, item)
	}
	fileListStr, err := json.Marshal(newFileList)
	if err != nil {
		return fmt.Errorf("将 fileList 转为 JSON 失败：%w", err)
	}
	helpers.BaiduPanLog.Debugf("移动文件列表：%s", string(fileListStr))

	resp, rerr := c.client.FilemanagerApi.Filemanagermove(ctx).AccessToken(c.accessToken).Async(0).Ondup("skip").Filelist(string(fileListStr)).Execute()
	return c.handleError(rerr, resp, nil)
}

// 复制
// path：包含文件名的完整路径
// newPath：新路径，不包含文件名
func (c *Client) Copy(ctx context.Context, path string, newPath string) error {
	if path == "" {
		return fmt.Errorf("路径不能为空")
	}
	if newPath == "" {
		return fmt.Errorf("新路径不能为空")
	}
	// 提取旧名字
	oldName := filepath.Base(path)
	// 构建请求参数
	fileList := []MoveOrCopyItem{
		{
			Path:    path,
			Dest:    newPath,
			NewName: oldName,
		},
	}
	fileListStr, err := json.Marshal(fileList)
	if err != nil {
		return fmt.Errorf("将 fileList 转为 JSON 失败：%w", err)
	}

	resp, rerr := c.client.FilemanagerApi.Filemanagercopy(ctx).AccessToken(c.accessToken).Async(0).Ondup("skip").Filelist(string(fileListStr)).Execute()
	return c.handleError(rerr, resp, nil)
}

// 批量复制
func (c *Client) CopyBatch(ctx context.Context, fileList []MoveOrCopyItem) error {
	if len(fileList) == 0 {
		return fmt.Errorf("文件列表不能为空")
	}
	fileListStr, err := json.Marshal(fileList)
	if err != nil {
		return fmt.Errorf("将 fileList 转为 JSON 失败：%w", err)
	}

	resp, rerr := c.client.FilemanagerApi.Filemanagercopy(ctx).AccessToken(c.accessToken).Async(0).Ondup("skip").Filelist(string(fileListStr)).Execute()
	return c.handleError(rerr, resp, nil)
}
