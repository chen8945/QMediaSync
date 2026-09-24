// updater/github_updater.go
package updater

import (
	"encoding/json"
	"fmt"
	"net/http"
	"runtime"
	"sort"
	"strings"
	"time"

	"qmediasync/internal/github"
	"qmediasync/internal/helpers"

	"github.com/hashicorp/go-version"
)

// 扩展 GitHubRelease 结构体
type GitHubRelease struct {
	TagName     string  `json:"tag_name"`
	Name        string  `json:"name"`
	Body        string  `json:"body"`
	Draft       bool    `json:"draft"`
	Prerelease  bool    `json:"prerelease"`
	PublishedAt string  `json:"published_at"`
	Assets      []Asset `json:"assets"`
	HTMLURL     string  `json:"html_url"` // 添加 Release 页面 URL
}

// ReleaseInfo 简化的版本信息，用于返回给调用方
type ReleaseInfo struct {
	Version      string    `json:"version"`
	Name         string    `json:"name"`
	ReleaseNotes string    `json:"release_notes"`
	PublishedAt  time.Time `json:"published_at"`
	IsPrerelease bool      `json:"is_prerelease"`
	IsDraft      bool      `json:"is_draft"`
	PageURL      string    `json:"page_url"`
}

// GitHubUpdater 扩展结构体
type GitHubUpdater struct {
	Owner             string
	Repo              string
	CurrentVersion    string
	HTTPClient        *http.Client
	IncludePreRelease bool // 是否包含预发布版本
}

type Asset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Size               int64  `json:"size"`
	DownloadCount      int    `json:"download_count"`
	ContentType        string `json:"content_type"`
}

// UpdateInfo 更新信息
type UpdateInfo struct {
	LatestVersion  string
	CurrentVersion string
	ReleaseNotes   string
	DownloadURL    string
	Checksum       string
	PublishedAt    time.Time
	HasUpdate      bool
}

// NewGitHubUpdater 创建新的 GitHub 更新器
func NewGitHubUpdater(owner, repo, currentVersion string) *GitHubUpdater {
	return &GitHubUpdater{
		Owner:          owner,
		Repo:           repo,
		CurrentVersion: currentVersion,
		HTTPClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// CheckForUpdate 检查更新（兼容现有代码）
func (g *GitHubUpdater) CheckForUpdate() (*UpdateInfo, error) {
	releases, err := g.GetLatestReleases(1) // 只获取最新的一个版本
	if err != nil {
		return nil, fmt.Errorf("获取最新 Release 失败：%w", err)
	}

	if len(releases) == 0 {
		return nil, fmt.Errorf("未找到 Release")
	}

	latestRelease := releases[0]

	// 比较版本
	currentVer, err := version.NewVersion(g.CurrentVersion)
	if err != nil {
		return nil, fmt.Errorf("当前版本号无效：%w", err)
	}

	latestVer, err := version.NewVersion(latestRelease.Version)
	if err != nil {
		return nil, fmt.Errorf("最新版本号无效：%w", err)
	}

	hasUpdate := latestVer.GreaterThan(currentVer)

	// 获取完整的 release 信息来查找资源文件
	fullReleases, err := g.getReleases()
	if err != nil {
		return nil, err
	}

	var downloadURL, checksum string
	for _, release := range fullReleases {
		if release.TagName == latestRelease.Version {
			downloadURL, checksum = g.findMatchingAsset(release.Assets)
			break
		}
	}

	return &UpdateInfo{
		LatestVersion:  latestRelease.Version,
		CurrentVersion: g.CurrentVersion,
		ReleaseNotes:   latestRelease.ReleaseNotes,
		DownloadURL:    downloadURL,
		Checksum:       checksum,
		PublishedAt:    latestRelease.PublishedAt,
		HasUpdate:      hasUpdate,
	}, nil
}

// getReleases 获取所有 Release
func (g *GitHubUpdater) getReleases() ([]GitHubRelease, error) {
	apiUrl := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases", g.Owner, g.Repo)

	// 使用 GitHub 管理器获取最佳客户端
	githubManager := github.GetManager()
	client, err := githubManager.GetClient()
	if err != nil {
		return nil, fmt.Errorf("获取 GitHub 客户端失败：%w", err)
	}

	// 使用管理器返回的客户端发起请求
	req, err := http.NewRequest("GET", apiUrl, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	req.Header.Set("User-Agent", g.Repo+"-updater")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub API 返回状态码：%d", resp.StatusCode)
	}

	var releases []GitHubRelease
	if err := json.NewDecoder(resp.Body).Decode(&releases); err != nil {
		return nil, err
	}

	return releases, nil
}

// findMatchingAsset 查找匹配的资源文件
func (g *GitHubUpdater) findMatchingAsset(assets []Asset) (string, string) {
	goos := runtime.GOOS
	goarch := runtime.GOARCH
	if goarch == "amd64" {
		goarch = "x86_64"
	}
	filename := fmt.Sprintf("qmediasync_%s_%s", goos, goarch)
	if goos == "windows" {
		filename += ".zip"
	} else {
		filename += ".tar.gz"
	}
	helpers.AppLogger.Infof("查找资源文件：%s", filename)
	var downloadURL, checksumURL string

	for _, asset := range assets {
		name := strings.ToLower(asset.Name)
		// 查找匹配的二进制文件
		helpers.AppLogger.Infof("匹配资源：%s => %s", name, filename)
		if downloadURL == "" && strings.Contains(name, filename) {
			downloadURL = asset.BrowserDownloadURL
		}
		if name == ChecksumsAssetName {
			checksumURL = asset.BrowserDownloadURL
		}
	}

	return downloadURL, checksumURL
}

// ChecksumsAssetName 是 release 流水线发布的 sha256sum 格式校验文件名。
const ChecksumsAssetName = "checksums.txt"

// GetLatestReleases 获取最新的 release 版本
func (g *GitHubUpdater) GetLatestReleases(limit int) ([]ReleaseInfo, error) {
	releases, err := g.getReleases()
	if err != nil {
		return nil, fmt.Errorf("获取 Release 列表失败：%w", err)
	}

	// 过滤和排序版本
	filteredReleases := g.filterAndSortReleases(releases, limit)

	// 转换为简化格式
	result := make([]ReleaseInfo, 0, len(filteredReleases))
	for _, release := range filteredReleases {
		publishedAt, _ := time.Parse(time.RFC3339, release.PublishedAt)

		result = append(result, ReleaseInfo{
			Version:      release.TagName,
			Name:         release.Name,
			ReleaseNotes: release.Body, //truncateString(release.Body, 200), // 截断过长的发布说明
			PublishedAt:  publishedAt,
			IsPrerelease: release.Prerelease,
			IsDraft:      release.Draft,
			PageURL:      release.HTMLURL,
		})
	}

	return result, nil
}

// GetLatestStableReleases 只获取稳定版本
func (g *GitHubUpdater) GetLatestStableReleases(limit int) ([]ReleaseInfo, error) {
	// 临时设置不包含预发布版本
	originalSetting := g.IncludePreRelease
	g.IncludePreRelease = false
	defer func() { g.IncludePreRelease = originalSetting }()

	return g.GetLatestReleases(limit)
}

// filterAndSortReleases 过滤和排序 Release
func (g *GitHubUpdater) filterAndSortReleases(releases []GitHubRelease, limit int) []GitHubRelease {
	filtered := make([]GitHubRelease, 0, len(releases))

	for _, release := range releases {
		// 跳过草稿
		if release.Draft {
			continue
		}

		// 根据设置决定是否包含预发布版本
		if !g.IncludePreRelease && release.Prerelease {
			continue
		}

		filtered = append(filtered, release)
	}

	// 按发布时间降序排序（最新的在前）
	sort.Slice(filtered, func(i, j int) bool {
		timeI, _ := time.Parse(time.RFC3339, filtered[i].PublishedAt)
		timeJ, _ := time.Parse(time.RFC3339, filtered[j].PublishedAt)
		return timeI.After(timeJ)
	})

	// 限制返回数量
	if len(filtered) > limit {
		filtered = filtered[:limit]
	}

	return filtered
}

// GetReleaseDownloadURL 根据版本号获取下载链接
func (g *GitHubUpdater) GetReleaseDownloadURL(versionTag string) (string, string, *ReleaseInfo, error) {
	// 获取所有 Release
	releases, err := g.getReleases()
	if err != nil {
		return "", "", nil, fmt.Errorf("获取 Release 列表失败：%w", err)
	}
	helpers.AppLogger.Infof("获取到 %d 个 Release", len(releases))
	// 查找指定版本
	var targetRelease *GitHubRelease
	for i := range releases {
		helpers.AppLogger.Infof("检查版本：%s", releases[i].TagName)
		if releases[i].TagName == versionTag {
			targetRelease = &releases[i]
			break
		}
	}

	if targetRelease == nil {
		return "", "", nil, fmt.Errorf("未找到版本：%s", versionTag)
	}

	// 查找匹配的资产文件
	downloadURL, checksumURL := g.findMatchingAsset(targetRelease.Assets)

	if downloadURL == "" {
		return "", "", nil, fmt.Errorf("未找到适合当前系统的下载文件")
	}

	// 解析发布时间
	publishedAt, _ := time.Parse(time.RFC3339, targetRelease.PublishedAt)

	// 创建 ReleaseInfo
	releaseInfo := &ReleaseInfo{
		Version:      targetRelease.TagName,
		Name:         targetRelease.Name,
		ReleaseNotes: targetRelease.Body,
		PublishedAt:  publishedAt,
		IsPrerelease: targetRelease.Prerelease,
		IsDraft:      targetRelease.Draft,
		PageURL:      targetRelease.HTMLURL,
	}

	return downloadURL, checksumURL, releaseInfo, nil
}
