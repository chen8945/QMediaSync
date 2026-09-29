package requests

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
	"qmediasync/internal/validation"
)

var versionPattern = regexp.MustCompile(`^v?\d+\.\d+\.\d+(?:[-+][0-9A-Za-z.-]+)?$`)

// PaginationRequest 分页请求。
type PaginationRequest struct {
	Page     int `json:"page" form:"page"`
	PageSize int `json:"page_size" form:"page_size"`
}

// Normalize 规范化通用分页请求。
func (r *PaginationRequest) Normalize(defaultPageSize int) error {
	if r.Page == 0 {
		r.Page = 1
	}
	if r.PageSize == 0 {
		r.PageSize = defaultPageSize
	}
	if err := validation.RangeInt("page", r.Page, 1, 1<<30); err != nil {
		return err
	}
	return validation.RangeInt("page_size", r.PageSize, 1, 100)
}

// NormalizeFileList 规范化 115/网盘文件列表分页请求。
func (r *PaginationRequest) NormalizeFileList() error {
	if r.Page == 0 {
		r.Page = 1
	}
	if r.PageSize == 0 {
		r.PageSize = 1150
	}
	if err := validation.RangeInt("page", r.Page, 1, 1<<30); err != nil {
		return err
	}
	return validation.RangeInt("page_size", r.PageSize, 100, 1150)
}

// NormalizeNetFileUI 规范化网盘文件浏览器 UI 分页请求。
func (r *PaginationRequest) NormalizeNetFileUI() error {
	if r.Page == 0 {
		r.Page = 1
	}
	if r.PageSize == 0 {
		r.PageSize = 50
	}
	if err := validation.RangeInt("page", r.Page, 1, 1<<30); err != nil {
		return err
	}
	switch r.PageSize {
	case 50, 100, 200, 500:
		return nil
	default:
		return validation.New("page_size", "必须是 50、100、200 或 500")
	}
}

// PositiveIDRequest 正 ID 请求。
type PositiveIDRequest struct {
	ID uint `json:"id" form:"id"`
}

// Validate 校验正 ID 请求。
func (r PositiveIDRequest) Validate() error {
	return validation.PositiveID("id", r.ID)
}

// ParsePositiveIDRequest 解析路径中的正 ID。
func ParsePositiveIDRequest(rawID string) (PositiveIDRequest, error) {
	rawID = strings.TrimSpace(rawID)
	if rawID == "" {
		return PositiveIDRequest{}, validation.New("id", "不能为空")
	}
	id, err := parsePositiveUint("id", rawID)
	if err != nil {
		return PositiveIDRequest{}, err
	}
	return PositiveIDRequest{ID: id}, nil
}

// IDListRequest ID 列表请求。
type IDListRequest struct {
	IDs []uint `json:"ids" form:"ids"`
}

// Validate 校验 ID 列表请求。
func (r IDListRequest) Validate() error {
	if len(r.IDs) == 0 {
		return validation.New("ids", "不能为空")
	}
	for _, id := range r.IDs {
		if err := validation.PositiveID("ids", id); err != nil {
			return err
		}
	}
	return nil
}

// NormalizedIDs 返回去重后的 ID 列表。
func (r IDListRequest) NormalizedIDs() []uint {
	seen := make(map[uint]bool, len(r.IDs))
	result := make([]uint, 0, len(r.IDs))
	for _, id := range r.IDs {
		if seen[id] {
			continue
		}
		seen[id] = true
		result = append(result, id)
	}
	return result
}

// IDCSVRequest 逗号分隔 ID 列表请求。
type IDCSVRequest struct {
	IDs       string `json:"ids" form:"ids"`
	parsedIDs []uint
}

// Validate 校验逗号分隔 ID 列表请求。
func (r *IDCSVRequest) Validate() error {
	rawIDs := strings.TrimSpace(r.IDs)
	if rawIDs == "" {
		return validation.New("ids", "不能为空")
	}
	parts := strings.Split(rawIDs, ",")
	ids := make([]uint, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			return validation.New("ids", "格式不正确")
		}
		id, err := parsePositiveUint("ids", part)
		if err != nil {
			return err
		}
		ids = append(ids, id)
	}
	req := IDListRequest{IDs: ids}
	if err := req.Validate(); err != nil {
		return err
	}
	r.parsedIDs = req.NormalizedIDs()
	return nil
}

// NormalizedIDs 返回去重后的 ID 列表。
func (r IDCSVRequest) NormalizedIDs() []uint {
	return r.parsedIDs
}

func parsePositiveUint(field string, raw string) (uint, error) {
	id, err := strconv.ParseUint(raw, 10, strconv.IntSize)
	if err != nil {
		return 0, validation.New(field, "格式不正确")
	}
	value := uint(id)
	if err := validation.PositiveID(field, value); err != nil {
		return 0, err
	}
	return value, nil
}

func validateIDItems(field string, ids []uint) error {
	for _, id := range ids {
		if err := validation.PositiveID(field, id); err != nil {
			return err
		}
	}
	return nil
}

// PathListRequest 路径列表请求。
type PathListRequest struct {
	SortBy       string            `json:"sort_by" form:"sort_by"`
	SortOrder    string            `json:"sort_order" form:"sort_order"`
	FoldersFirst *bool             `json:"folders_first" form:"folders_first"`
	Refresh      int               `json:"refresh" form:"refresh"`
	ParentID     string            `json:"parent_id" form:"parent_id"`
	ParentPath   string            `json:"parent_path" form:"parent_path"`
	SourceType   models.SourceType `json:"source_type" form:"source_type"`
	AccountID    uint              `json:"account_id" form:"account_id"`
	PaginationRequest
}

// Validate 校验路径列表请求。
func (r *PathListRequest) Validate() error {
	if err := validateSourceType(r.SourceType); err != nil {
		return err
	}
	if r.SourceType != models.SourceTypeLocal {
		if err := validation.PositiveID("account_id", r.AccountID); err != nil {
			return err
		}
	}
	if r.Refresh != 0 && r.Refresh != 1 {
		return validation.New("refresh", "仅支持 0 或 1")
	}
	return ValidateBrowseSort(r.SourceType, "directories", r.SortBy, r.SortOrder, r.FoldersFirst)
}

// NetFileListRequest 网盘文件列表请求。
type NetFileListRequest struct {
	FoldersFirst *bool  `json:"folders_first" form:"folders_first"`
	ParentID     string `json:"parent_id" form:"path"`
	AccountID    uint   `json:"account_id" form:"account_id"`
	Refresh      bool   `json:"refresh" form:"refresh"`
	SortBy       string `json:"sort_by" form:"sort_by"`
	SortOrder    string `json:"sort_order" form:"sort_order"`
	PaginationRequest
}

// Validate 校验网盘文件列表请求。
func (r *NetFileListRequest) Validate() error {
	if err := validation.PositiveID("account_id", r.AccountID); err != nil {
		return err
	}
	if err := r.PaginationRequest.NormalizeNetFileUI(); err != nil {
		return err
	}
	if r.SortOrder == "" {
		r.SortOrder = "asc"
	}
	switch r.SortBy {
	case "", "default", "name", "time", "size", "type":
	default:
		return validation.New("sort_by", "不支持的排序字段")
	}
	switch r.SortOrder {
	case "asc", "desc":
	default:
		return validation.New("sort_order", "不支持的排序方向")
	}
	return nil
}

// CreateDirRequest 创建目录请求。
type CreateDirRequest struct {
	ParentID   string            `json:"parent_id" form:"parent_id"`
	ParentPath string            `json:"parent_path" form:"parent_path"`
	SourceType models.SourceType `json:"source_type" form:"source_type"`
	AccountID  uint              `json:"account_id" form:"account_id"`
	Name       string            `json:"name" form:"name"`
}

// Validate 校验创建目录请求。
func (r CreateDirRequest) Validate() error {
	if err := validateSourceType(r.SourceType); err != nil {
		return err
	}
	if r.SourceType != models.SourceTypeLocal {
		if err := validation.PositiveID("account_id", r.AccountID); err != nil {
			return err
		}
	}
	return validateFolderName(r.Name)
}

// DeleteDirRequest 删除远程目录请求。
type DeleteDirRequest struct {
	ParentID  string `json:"parent_id" form:"parent_id"`
	FileID    string `json:"file_id" form:"file_id"`
	AccountID uint   `json:"account_id" form:"account_id"`
}

// Validate 校验删除远程目录请求。
func (r DeleteDirRequest) Validate() error {
	if err := validation.PositiveID("account_id", r.AccountID); err != nil {
		return err
	}
	if strings.TrimSpace(r.FileID) == "" || r.FileID == "0" {
		return validation.New("file_id", "不能为空")
	}
	return nil
}

// maxBatchFileIDs 限制单次批量操作的文件数量，对齐文件列表单页上限。
const maxBatchFileIDs = 500

// DeleteFilesRequest 批量删除远程文件请求。
type DeleteFilesRequest struct {
	ParentID  string   `json:"parent_id" form:"parent_id"`
	FileIDs   []string `json:"file_ids"`
	AccountID uint     `json:"account_id" form:"account_id"`
}

// Validate 校验批量删除远程文件请求。
func (r DeleteFilesRequest) Validate() error {
	if err := validation.PositiveID("account_id", r.AccountID); err != nil {
		return err
	}
	return validateFileIDs(r.FileIDs)
}

// NormalizedFileIDs 按来源规范化并去重；路径型 ID 保留原始空白。
func (r DeleteFilesRequest) NormalizedFileIDs(sourceType models.SourceType) []string {
	return normalizeFileIDs(r.FileIDs, sourceType)
}

// MoveFilesRequest 批量移动远程文件请求。
type MoveFilesRequest struct {
	ParentID       string   `json:"parent_id" form:"parent_id"`
	FileIDs        []string `json:"file_ids"`
	TargetParentID string   `json:"target_parent_id" form:"target_parent_id"`
	AccountID      uint     `json:"account_id" form:"account_id"`
}

// Validate 校验批量移动远程文件请求。
func (r MoveFilesRequest) Validate() error {
	return validateFileTransferRequest(r.AccountID, r.TargetParentID, r.FileIDs)
}

// NormalizedFileIDs 按来源规范化并去重；路径型 ID 保留原始空白。
func (r MoveFilesRequest) NormalizedFileIDs(sourceType models.SourceType) []string {
	return normalizeFileIDs(r.FileIDs, sourceType)
}

// CopyFilesRequest 批量复制远程文件请求。
type CopyFilesRequest = MoveFilesRequest

// RenameFileRequest 重命名远程文件请求。
type RenameFileRequest struct {
	ParentID  string `json:"parent_id" form:"parent_id"`
	FileID    string `json:"file_id" form:"file_id"`
	NewName   string `json:"new_name" form:"new_name"`
	AccountID uint   `json:"account_id" form:"account_id"`
}

// Validate 校验重命名远程文件请求。
func (r RenameFileRequest) Validate() error {
	if err := validation.PositiveID("account_id", r.AccountID); err != nil {
		return err
	}
	if strings.TrimSpace(r.FileID) == "" || r.FileID == "0" {
		return validation.New("file_id", "不能为空")
	}
	return validateFileEntryName("new_name", r.NewName, "文件名不合法")
}

// validateFileIDs 校验批量文件 ID 列表：非空、不超上限且不含无效 ID。
func validateFileIDs(fileIDs []string) error {
	if len(fileIDs) == 0 {
		return validation.New("file_ids", "不能为空")
	}
	if len(fileIDs) > maxBatchFileIDs {
		return validation.New("file_ids", fmt.Sprintf("单次最多操作 %d 项", maxBatchFileIDs))
	}
	for _, fileID := range fileIDs {
		trimmed := strings.TrimSpace(fileID)
		if trimmed == "" || trimmed == "0" || fileID == "/" {
			return validation.New("file_ids", "包含无效的文件 ID")
		}
	}
	return nil
}

// validateFileTransferRequest 校验批量移动 / 复制请求的公共字段。
func validateFileTransferRequest(accountID uint, targetParentID string, fileIDs []string) error {
	if err := validation.PositiveID("account_id", accountID); err != nil {
		return err
	}
	if strings.TrimSpace(targetParentID) == "" {
		return validation.New("target_parent_id", "不能为空")
	}
	return validateFileIDs(fileIDs)
}

// normalizeFileIDs 仅裁剪 115 数字 ID 的空白，并按原始顺序去重。
func normalizeFileIDs(fileIDs []string, sourceType models.SourceType) []string {
	seen := make(map[string]bool, len(fileIDs))
	result := make([]string, 0, len(fileIDs))
	for _, fileID := range fileIDs {
		if sourceType == models.SourceType115 {
			fileID = strings.TrimSpace(fileID)
		}
		if fileID == "" || seen[fileID] {
			continue
		}
		seen[fileID] = true
		result = append(result, fileID)
	}
	return result
}

// FNPathRequest 飞牛路径授权回调请求。
type FNPathRequest struct {
	Path string `json:"path" form:"path"`
}

// Validate 校验飞牛路径授权回调请求。
func (r FNPathRequest) Validate() error {
	return validation.NonBlank("path", r.Path)
}

// QueueListRequest 队列分页请求。
type QueueListRequest struct {
	Status int `json:"status" form:"status"`
	PaginationRequest
}

// Validate 校验队列分页请求。
func (r *QueueListRequest) Validate() error {
	return r.PaginationRequest.Normalize(100)
}

// ManualSyncRequest 手动同步请求。
type ManualSyncRequest struct {
	PathID     string `form:"path_id" json:"path_id"`
	Path       string `form:"path" json:"path"`
	TargetPath string `form:"target_path" json:"target_path"`
	IsFile     bool   `form:"is_file" json:"is_file"`
	AccountID  uint   `form:"account_id" json:"account_id"`
}

// Validate 校验手动同步请求。
func (r ManualSyncRequest) Validate() error {
	if err := validation.NonBlank("path_id", r.PathID); err != nil {
		return err
	}
	if err := validation.NonBlank("target_path", r.TargetPath); err != nil {
		return err
	}
	return validation.PositiveID("account_id", r.AccountID)
}

// SaveRelScrapePathRequest 保存同步路径和刮削路径关联请求。
type SaveRelScrapePathRequest struct {
	SyncPathID          uint   `json:"sync_path_id" form:"sync_path_id"`
	LegacySyncPathID    uint   `json:"id" form:"id"`
	ScrapePathIDs       []uint `json:"scrape_path_ids" form:"scrape_path_ids"`
	LegacyScrapePathIDs []uint `json:"scrape_path_id" form:"scrape_path_id"`
}

// Validate 校验同步路径和刮削路径关联请求。
func (r *SaveRelScrapePathRequest) Validate() error {
	if r.SyncPathID == 0 {
		r.SyncPathID = r.LegacySyncPathID
	}
	if r.ScrapePathIDs == nil {
		r.ScrapePathIDs = r.LegacyScrapePathIDs
	}
	if err := validation.PositiveID("sync_path_id", r.SyncPathID); err != nil {
		return err
	}
	return validateIDItems("scrape_path_ids", r.ScrapePathIDs)
}

// SaveScrapeStrmPathRequest 保存刮削路径关联的 STRM 路径请求。
type SaveScrapeStrmPathRequest struct {
	ScrapePathID uint   `json:"scrape_path_id" form:"scrape_path_id"`
	SyncPathIDs  []uint `json:"sync_path_ids" form:"sync_path_ids"`
}

// Validate 校验刮削路径关联的 STRM 路径请求。
func (r SaveScrapeStrmPathRequest) Validate() error {
	if err := validation.PositiveID("scrape_path_id", r.ScrapePathID); err != nil {
		return err
	}
	return validateIDItems("sync_path_ids", r.SyncPathIDs)
}

// RescrapeRequest 重新刮削请求。
type RescrapeRequest struct {
	ID      uint   `json:"id" form:"id"`
	Name    string `json:"name" form:"name"`
	Year    int    `json:"year" form:"year"`
	TmdbID  int64  `json:"tmdb_id" form:"tmdb_id"`
	Season  int    `json:"season" form:"season"`
	Episode int    `json:"episode" form:"episode"`
}

// Validate 校验重新刮削请求。
func (r RescrapeRequest) Validate() error {
	if err := validation.PositiveID("id", r.ID); err != nil {
		return err
	}
	if r.Year != 0 {
		return validation.RangeInt("year", r.Year, 1900, 2100)
	}
	return nil
}

// OldLogsRequest 读取旧日志请求。
type OldLogsRequest struct {
	Path      string `json:"path" form:"path"`
	Pos       int64  `json:"pos" form:"pos"`
	Limit     int    `json:"limit" form:"limit"`
	Direction string `json:"direction" form:"direction"`
}

// Validate 校验读取旧日志请求。
func (r *OldLogsRequest) Validate() error {
	if err := validateLogPath(r.Path); err != nil {
		return err
	}
	if r.Limit == 0 {
		r.Limit = 100
	}
	if err := validation.RangeInt("limit", r.Limit, 1, 1000); err != nil {
		return err
	}
	if r.Direction == "" {
		r.Direction = "forward"
	}
	return validation.OneOfString("direction", r.Direction, []string{"forward", "backward"})
}

// LogFileRequest 日志文件请求。
type LogFileRequest struct {
	Path string `json:"path" form:"path"`
}

// Validate 校验日志文件请求。
func (r LogFileRequest) Validate() error {
	return validateLogPath(r.Path)
}

// TmpImageRequest 临时图片请求。
type TmpImageRequest struct {
	Path      string           `json:"path" form:"path"`
	MediaType models.MediaType `json:"type" form:"type"`
}

// Validate 校验临时图片请求。
func (r TmpImageRequest) Validate() error {
	if err := validateRelativePath("path", r.Path); err != nil {
		return err
	}
	return validation.OneOfString("type", string(r.MediaType), []string{
		string(models.MediaTypeMovie),
		string(models.MediaTypeTvShow),
	})
}

// UpdateVersionRequest 更新版本请求。
type UpdateVersionRequest struct {
	Version string `json:"version"`
	Channel string `json:"channel"`
}

// Validate 校验更新版本请求。
func (r *UpdateVersionRequest) Validate() error {
	r.Version = strings.TrimSpace(r.Version)
	if r.Version == "" {
		return validation.New("version", "不能为空")
	}
	if !versionPattern.MatchString(r.Version) {
		return validation.New("version", "版本号格式不正确")
	}
	r.Channel = strings.TrimSpace(r.Channel)
	if r.Channel == "" {
		r.Channel = "github"
	}
	return validation.OneOfString("channel", r.Channel, []string{"github"})
}

// QueueStatsRequest 队列统计请求。
type QueueStatsRequest struct {
	TimeWindow int64  `json:"time_window" form:"time_window"`
	StartDate  string `json:"start_date" form:"start_date"`
	EndDate    string `json:"end_date" form:"end_date"`
}

// Validate 校验队列统计请求。
func (r *QueueStatsRequest) Validate() error {
	if r.TimeWindow == 0 {
		r.TimeWindow = 3600
	}
	if err := validation.RangeInt64("time_window", r.TimeWindow, 60, 604800); err != nil {
		return err
	}
	if r.StartDate == "" && r.EndDate == "" {
		return nil
	}
	startDate, err := time.ParseInLocation("2006-01-02", r.StartDate, time.Local)
	if err != nil {
		return validation.New("start_date", "格式必须是 YYYY-MM-DD")
	}
	endDate, err := time.ParseInLocation("2006-01-02", r.EndDate, time.Local)
	if err != nil {
		return validation.New("end_date", "格式必须是 YYYY-MM-DD")
	}
	if startDate.After(endDate) {
		return validation.New("start_date", "不能晚于结束日期")
	}
	if endDate.Sub(startDate) > 31*24*time.Hour {
		return validation.New("end_date", "日期范围不能超过 31 天")
	}
	return nil
}

func validateSourceType(sourceType models.SourceType) error {
	return validation.OneOfString("source_type", string(sourceType), []string{
		string(models.SourceTypeLocal),
		string(models.SourceType115),
		string(models.SourceType123),
		string(models.SourceTypeOpenList),
		string(models.SourceTypeBaiduPan),
	})
}

func validateFolderName(name string) error {
	return validateFileEntryName("name", name, "文件夹名不合法")
}

// validateFileEntryName 校验文件或目录条目名称：非空、合法且不含路径分隔符。
func validateFileEntryName(field string, name string, illegalMessage string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return validation.New(field, "不能为空")
	}
	if name == "." || name == ".." {
		return validation.New(field, illegalMessage)
	}
	if strings.ContainsAny(name, `/\`) {
		return validation.New(field, "不能包含路径分隔符")
	}
	for _, r := range name {
		if r < 32 || r == 127 {
			return validation.New(field, "不能包含控制字符")
		}
	}
	return nil
}

func validateLogPath(path string) error {
	if err := validateRelativePath("path", path); err != nil {
		return err
	}
	rawPath := strings.TrimSpace(path)
	if strings.Contains(rawPath, `\`) {
		return validation.New("path", "只能是日志文件名或同步任务日志目录下的日志文件")
	}
	for _, part := range strings.Split(rawPath, "/") {
		if part == "." || part == ".." {
			return validation.New("path", "不能包含路径穿越")
		}
	}

	cleaned := filepath.ToSlash(filepath.Clean(rawPath))
	parts := strings.Split(cleaned, "/")
	if len(parts) == 1 {
		return validateLogFileName(parts[0])
	}
	if isLogPathInDir(parts, helpers.SyncLogRelativeDir()) || isLogPathInDir(parts, helpers.LegacySyncLogRelativeDir()) {
		return validateLogFileName(parts[len(parts)-1])
	}
	return validation.New("path", "只能是日志文件名或同步任务日志目录下的日志文件")
}

func isLogPathInDir(parts []string, dir string) bool {
	dir = strings.Trim(strings.TrimSpace(filepath.ToSlash(filepath.Clean(dir))), "/")
	if dir == "" || dir == "." {
		return len(parts) == 1
	}
	dirParts := strings.Split(dir, "/")
	if len(parts) != len(dirParts)+1 {
		return false
	}
	for i, part := range dirParts {
		if parts[i] != part {
			return false
		}
	}
	return true
}

func validateLogFileName(name string) error {
	if name == "" || name == "." || name == ".." {
		return validation.New("path", "日志文件名不合法")
	}
	if strings.ContainsAny(name, `/\`) {
		return validation.New("path", "只能是日志文件名")
	}
	return nil
}

func validateRelativePath(field string, path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return validation.New(field, "不能为空")
	}
	if filepath.IsAbs(path) {
		return validation.New(field, "不能是绝对路径")
	}
	cleaned := filepath.Clean(path)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return validation.New(field, "不能包含路径穿越")
	}
	return nil
}
