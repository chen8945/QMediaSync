package openlist

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"qmediasync/internal/helpers"
)

type FileListItemInfo struct {
	Name        string            `json:"name"`
	Size        int64             `json:"size"`
	IsDir       bool              `json:"is_dir"`
	Modified    string            `json:"modified"`
	Created     string            `json:"created"`
	Sign        string            `json:"sign"`
	Thumb       string            `json:"thumb"`
	Type        int               `json:"type"`
	HashInfo    string            `json:"hashinfo"`
	ID          string            `json:"id"`
	HashInfoMap map[string]string `json:"hash_info"`
}

type FileListResp struct {
	Content  []FileListItemInfo `json:"content"`
	Total    int64              `json:"total"`
	Readme   string             `json:"readme"`
	Write    bool               `json:"write"`
	Provider string             `json:"provider"`
	Header   string             `json:"header"`
}

//	{
//	    "name": "Alist V3.md",
//	    "size": 2618,
//	    "is_dir": false,
//	    "modified": "2024-05-17T16:05:36.4651534+08:00",
//	    "created": "2024-05-17T16:05:29.2001008+08:00",
//	    "sign": "",
//	    "thumb": "",
//	    "type": 4,
//	    "hashinfo": "null",
//	    "hash_info": null,
//	    "raw_url": "http://127.0.0.1:5244/p/local/Alist%20V3.md",
//	    "readme": "",
//	    "header": "",
//	    "provider": "Local",
//	    "related": null
//	  }
type FileDetail struct {
	Name        string            `json:"name"`
	Size        int64             `json:"size"`
	IsDir       bool              `json:"is_dir"`
	Modified    string            `json:"modified"`
	Created     string            `json:"created"`
	Sign        string            `json:"sign"`
	Thumb       string            `json:"thumb"`
	Type        int               `json:"type"`
	HashInfo    string            `json:"hashinfo"`
	ID          string            `json:"id"`
	HashInfoMap map[string]string `json:"hash_info"`
	RawURL      string            `json:"raw_url"`
	Readme      string            `json:"readme"`
	Header      string            `json:"header"`
	Provider    string            `json:"provider"`
}

// KnownHashes 只读取 OpenList hash_info 中算法名明确的哈希值；hashinfo 是供应商私有字段，不能猜测其格式。
func KnownHashes(hashInfo map[string]string) (sha1 string, md5 string) {
	for algorithm, value := range hashInfo {
		switch strings.ToLower(strings.TrimSpace(algorithm)) {
		case "sha1":
			sha1 = strings.TrimSpace(value)
		case "md5":
			md5 = strings.TrimSpace(value)
		}
	}
	return sha1, md5
}

//	"task": {
//	      "id": "sdH2LbjyWRk",
//	      "name": "upload animated_zoom.gif to [/data](/openlist)",
//	      "state": 0,
//	      "status": "uploading",
//	      "progress": 0,
//	      "error": ""
//	    }
type UploadResult struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	State    int    `json:"state"`
	Status   string `json:"status"`
	Progress int    `json:"progress"`
	Error    string `json:"error"`
}

type UploadTask struct {
	Task UploadResult `json:"task"`
}

type DirRep struct {
	Name     string `json:"name"`
	Modified string `json:"modified"`
}

// 文件夹列表
// /api/fs/dirs
func (c *Client) DirList(path string, forceRoot bool) ([]DirRep, error) {
	path = strings.ReplaceAll(path, "\\", "/")
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	type fileListReq struct {
		Path      string `json:"path"`
		ForceRoot bool   `json:"force_root"`
	}
	reqData := &fileListReq{
		Path:      path,
		ForceRoot: forceRoot,
	}
	result := &Resp[[]DirRep]{}
	req := c.client.R().SetBody(reqData).SetMethod(http.MethodPost).SetResult(result)
	_, err := c.doRequest("/api/fs/dirs", req, nil)
	if err != nil {
		helpers.OpenListLog.Errorf("OpenList 获取文件夹列表失败：%s", err.Error())
		return nil, err
	}
	return result.Data, nil
}

// 文件列表
func (c *Client) FileList(ctx context.Context, path string, page int, perPage int) (*FileListResp, error) {
	return c.FileListWithRefresh(ctx, path, page, perPage, true)
}

func (c *Client) FileListWithRefresh(ctx context.Context, path string, page int, perPage int, refresh bool) (*FileListResp, error) {
	path = strings.ReplaceAll(path, "\\", "/")
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	type fileListReq struct {
		Path     string `json:"path"`
		Page     int    `json:"page"`
		PerPage  int    `json:"per_page"`
		Refresh  bool   `json:"refresh"`
		Password string `json:"password"`
	}
	reqData := &fileListReq{
		Path:     path,
		Page:     page,
		PerPage:  perPage,
		Refresh:  refresh,
		Password: "",
	}
	result := &Resp[FileListResp]{}
	req := c.client.R().SetBody(reqData).SetMethod(http.MethodPost).SetResult(result)
	req.SetContext(ctx)
	_, err := c.doRequest("/api/fs/list", req, nil)
	if err != nil {
		helpers.OpenListLog.Errorf("OpenList 获取文件列表失败：%s", err.Error())
		return nil, err
	}
	return &result.Data, nil
}

// 文件详情
func (c *Client) FileDetail(path string) (*FileDetail, error) {
	path = strings.ReplaceAll(path, "\\", "/")
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	type fileListReq struct {
		Path string `json:"path"`
	}
	reqData := &fileListReq{
		Path: path,
	}
	result := &Resp[FileDetail]{}
	req := c.client.R().SetBody(reqData).SetMethod(http.MethodPost).SetResult(result)
	_, err := c.doRequest("/api/fs/get", req, nil)
	if err != nil {
		helpers.OpenListLog.Errorf("OpenList 获取文件详情失败：%s", err.Error())
		return nil, err
	}
	return &result.Data, nil
}

// 上传
func (c *Client) Upload(localFile string, remotePath string) (*UploadResult, error) {
	remotePath = strings.ReplaceAll(remotePath, "\\", "/")
	if !strings.HasPrefix(remotePath, "/") {
		remotePath = "/" + remotePath
	}
	// info, _ := os.Stat(localFile)
	helpers.AppLogger.Infof("OpenList 上传文件：%s，目标路径：%s", localFile, remotePath)
	result := &Resp[UploadTask]{}
	req := c.client.R()
	req.SetFile("file", localFile)
	// 对远程路径进行 URL 编码
	encodedPath := helpers.UrlEncode(remotePath)
	req.Header.Add("File-Path", encodedPath)
	req.Header.Add("As-Task", "true")
	req.Header.Add("overwrite", "false")
	req.Header.Add("Content-Type", "multipart/form-data")
	req.SetMethod(http.MethodPut).SetResult(&result)
	_, err := c.doRequest("/api/fs/form", req, MakeRequestConfig(0, 1, 300))
	if err != nil {
		helpers.OpenListLog.Errorf("OpenList 上传文件失败：%s", err.Error())
		return nil, err
	}
	helpers.OpenListLog.Infof("OpenList 上传文件成功：%s", result.Data.Task.ID)
	return &result.Data.Task, nil
}

func (c *Client) UploadUseHttp(filePath string, remotePath string) (*UploadResult, error) {
	remotePath = strings.ReplaceAll(remotePath, "\\", "/")
	if !strings.HasPrefix(remotePath, "/") {
		remotePath = "/" + remotePath
	}
	// info, _ := os.Stat(filePath)
	// 打开本地文件
	file, err := os.Open(filePath)
	if err != nil {
		helpers.OpenListLog.Errorf("OpenList 上传文件失败：%s", err.Error())
		return nil, fmt.Errorf("打开本地文件失败：%w", err)
	}
	defer file.Close()

	fileName := filepath.Base(filePath)
	// URL 编码远程路径（保留斜杠，避免转义）
	encodedPath := helpers.UrlEncode(remotePath)
	// 构造上传请求 URL
	reqURL := fmt.Sprintf("%s/api/fs/form", c.BaseUrl)

	// 创建 multipart/form-data 请求体
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	// 添加文件字段（字段名"file"需与服务端一致）
	formFile, err := writer.CreateFormFile("file", fileName)
	if err != nil {
		return nil, fmt.Errorf("创建表单文件失败：%w", err)
	}
	// 复制文件内容到表单
	if _, copyErr := io.Copy(formFile, file); copyErr != nil {
		return nil, fmt.Errorf("复制文件到表单失败：%w", copyErr)
	}
	// 关闭 writer，确保边界符正确写入
	if closeErr := writer.Close(); closeErr != nil {
		return nil, fmt.Errorf("关闭表单写入器失败：%w", closeErr)
	}

	// 构造 HTTP 请求
	req, err := http.NewRequest("PUT", reqURL, body)
	if err != nil {
		return nil, fmt.Errorf("创建上传请求失败：%w", err)
	}
	// 设置请求头（Authorization、Content-Type、file-path）
	req.Header.Set("Authorization", c.AccessToken)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("File-Path", encodedPath)
	// 延长上传超时（大文件上传可能需要更长时间，此处设 5 分钟）
	req.Close = true
	client := &http.Client{
		Timeout: 5 * time.Minute,
	}
	// 发送上传请求
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("发送上传请求失败：%w", err)
	}
	defer resp.Body.Close()

	// 读取响应体
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("读取上传响应失败：%w", err)
	}
	helpers.OpenListLog.Infof("OpenList 上传文件响应：%s", string(respBody))
	// 解析上传响应
	result := &Resp[UploadTask]{}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("解析上传响应失败，响应体：%s，原因：%w", string(respBody), err)
	}

	// 检查上传结果
	if resp.StatusCode != http.StatusOK || result.Code != 200 {
		return nil, fmt.Errorf("上传失败，HTTP 状态码：%d，错误码：%d，消息：%s",
			resp.StatusCode, result.Code, result.Message)
	}

	return &result.Data.Task, nil
}

func (c *Client) GetRawUrl(filePath string) string {
	fileDetail, err := c.FileDetail(filePath)
	if err != nil {
		helpers.OpenListLog.Errorf("获取文件详情失败：%v", err)
		return ""
	}
	if fileDetail.RawURL == "" {
		helpers.OpenListLog.Errorf("文件详情中未找到直链")
		return ""
	}
	return fileDetail.RawURL
}

func (c *Client) Mkdir(path string) error {
	path = strings.ReplaceAll(path, "\\", "/")
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	type fileListReq struct {
		Path string `json:"path"`
	}
	reqData := &fileListReq{
		Path: path,
	}
	req := c.client.R().SetBody(reqData).SetMethod(http.MethodPost)
	_, err := c.doRequest("/api/fs/mkdir", req, nil)
	if err != nil {
		helpers.OpenListLog.Errorf("OpenList 创建目录失败：%s", err.Error())
		return err
	}
	return nil
}

func (c *Client) Move(oldPath, newPath string, names []string) error {
	oldPath = strings.ReplaceAll(oldPath, "\\", "/")
	if !strings.HasPrefix(oldPath, "/") {
		oldPath = "/" + oldPath
	}
	newPath = strings.ReplaceAll(newPath, "\\", "/")
	if !strings.HasPrefix(newPath, "/") {
		newPath = "/" + newPath
	}
	type fileListReq struct {
		OldPath string   `json:"src_dir"`
		NewPath string   `json:"dst_dir"`
		Names   []string `json:"names"`
	}
	reqData := &fileListReq{
		OldPath: oldPath,
		NewPath: newPath,
		Names:   names,
	}
	result := &Resp[any]{}
	req := c.client.R().SetBody(reqData).SetMethod(http.MethodPost).SetResult(result)
	_, err := c.doRequest("/api/fs/move", req, nil)
	if err != nil {
		helpers.OpenListLog.Errorf("OpenList 移动文件失败：%s", err.Error())
		return err
	}
	if result.Code != 200 {
		return fmt.Errorf("%s", result.Message)
	}
	return nil
}

func (c *Client) Copy(oldPath, newPath string, names []string) error {
	oldPath = strings.ReplaceAll(oldPath, "\\", "/")
	if !strings.HasPrefix(oldPath, "/") {
		oldPath = "/" + oldPath
	}
	newPath = strings.ReplaceAll(newPath, "\\", "/")
	if !strings.HasPrefix(newPath, "/") {
		newPath = "/" + newPath
	}
	type fileListReq struct {
		OldPath string   `json:"src_dir"`
		NewPath string   `json:"dst_dir"`
		Names   []string `json:"names"`
	}
	reqData := &fileListReq{
		OldPath: oldPath,
		NewPath: newPath,
		Names:   names,
	}
	result := &Resp[any]{}
	req := c.client.R().SetBody(reqData).SetMethod(http.MethodPost).SetResult(result)
	_, err := c.doRequest("/api/fs/copy", req, nil)
	if err != nil {
		helpers.OpenListLog.Errorf("OpenList 复制文件失败：%s", err.Error())
		return err
	}
	if result.Code != 200 {
		return fmt.Errorf("%s", result.Message)
	}
	return nil
}

func (c *Client) Rename(path, oldName, newName string) error {
	path = strings.ReplaceAll(path, "\\", "/")
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	type fileListReq struct {
		SrcDir        string `json:"src_dir"`
		RenameObjects []struct {
			SrcName string `json:"src_name"`
			NewName string `json:"new_name"`
		} `json:"rename_objects"`
	}
	reqData := &fileListReq{
		SrcDir: path,
		RenameObjects: []struct {
			SrcName string `json:"src_name"`
			NewName string `json:"new_name"`
		}{
			{
				SrcName: oldName,
				NewName: newName,
			},
		},
	}
	result := &Resp[any]{}
	req := c.client.R().SetBody(reqData).SetMethod(http.MethodPost).SetResult(result)
	_, err := c.doRequest("/api/fs/batch_rename", req, nil)
	if err != nil {
		helpers.OpenListLog.Errorf("OpenList 重命名文件失败：%s", err.Error())
		return err
	}
	if result.Code != 200 {
		return fmt.Errorf("%s", result.Message)
	}
	return nil
}

func (c *Client) Del(path string, names []string) error {
	path = strings.ReplaceAll(path, "\\", "/")
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	type fileListReq struct {
		Dir   string   `json:"dir"`
		Names []string `json:"names"`
	}
	reqData := &fileListReq{
		Dir:   path,
		Names: names,
	}
	result := &Resp[any]{}
	req := c.client.R().SetBody(reqData).SetMethod(http.MethodPost).SetResult(result)
	_, err := c.doRequest("/api/fs/remove", req, nil)
	if err != nil {
		helpers.OpenListLog.Errorf("OpenList 删除目录失败：%s", err.Error())
		return err
	}
	if result.Code != 200 {
		return fmt.Errorf("%s", result.Message)
	}
	return nil
}
