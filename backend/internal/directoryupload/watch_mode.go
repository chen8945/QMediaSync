package directoryupload

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"qmediasync/internal/models"
)

const (
	watchModeInotifyUsageThresholdNumerator   = 8
	watchModeInotifyUsageThresholdDenominator = 10
)

type watchModeDecision struct {
	Mode   RuleRuntimeMode
	Reason string
	Err    error
}

type inotifyLimits struct {
	Available         bool
	MaxUserWatches    int
	MaxUserInstances  int
	CurrentWatches    int
	CurrentInstances  int
	CurrentUsageError error
}

type watchModeDetector interface {
	FilesystemType(ctx context.Context, monitorPath string) (string, error)
	InotifyLimits(ctx context.Context) (inotifyLimits, error)
	WatchDirectoryCount(ctx context.Context, rule *models.DirectoryUploadRule) (int, error)
}

type osWatchModeDetector struct {
	goos          string
	procRoot      string
	mountInfoPath string
}

func newOSWatchModeDetector() *osWatchModeDetector {
	return newOSWatchModeDetectorForGOOS(runtime.GOOS)
}

func newOSWatchModeDetectorForGOOS(goos string) *osWatchModeDetector {
	return &osWatchModeDetector{
		goos:     goos,
		procRoot: "/proc",
	}
}

func decideWatchMode(ctx context.Context, rule *models.DirectoryUploadRule, detector watchModeDetector) watchModeDecision {
	if err := terminalContextError(ctx, nil); err != nil {
		return watchModeDecision{
			Reason: "watch mode 检测已取消",
			Err:    err,
		}
	}
	if detector == nil {
		detector = newOSWatchModeDetector()
	}
	if rule == nil {
		return watchModeDecision{
			Mode:   RuleRuntimeModeWatcher,
			Reason: "目录监控规则为空，继续尝试 fsnotify",
		}
	}

	reasons := []string{}
	filesystemType, err := detector.FilesystemType(ctx, rule.MonitorPath)
	if cancelErr := terminalContextError(ctx, err); cancelErr != nil {
		return watchModeDecision{
			Reason: "文件系统类型检测已取消",
			Err:    cancelErr,
		}
	} else if err != nil {
		reasons = append(reasons, fmt.Sprintf("文件系统类型检测失败，继续尝试 fsnotify：%v", err))
	} else if isNetworkOrFuseFilesystem(filesystemType) {
		return watchModeDecision{
			Mode:   RuleRuntimeModePolling,
			Reason: fmt.Sprintf("检测到网络或 FUSE 文件系统 %s，使用 polling", filesystemType),
		}
	}

	limits, err := detector.InotifyLimits(ctx)
	if cancelErr := terminalContextError(ctx, err); cancelErr != nil {
		return watchModeDecision{
			Reason: "inotify 限额检测已取消",
			Err:    cancelErr,
		}
	} else if err != nil {
		reasons = append(reasons, fmt.Sprintf("inotify 限额检测失败，继续尝试 fsnotify：%v", err))
		return watchModeDecision{Mode: RuleRuntimeModeWatcher, Reason: strings.Join(reasons, "；")}
	}
	if !limits.Available {
		reasons = append(reasons, "非 Linux 环境不读取 /proc，继续尝试 fsnotify")
		return watchModeDecision{Mode: RuleRuntimeModeWatcher, Reason: strings.Join(reasons, "；")}
	}
	if limits.MaxUserWatches <= 0 || limits.MaxUserInstances <= 0 {
		reasons = append(reasons, fmt.Sprintf(
			"inotify 限额无效（max_user_watches=%d max_user_instances=%d），继续尝试 fsnotify",
			limits.MaxUserWatches,
			limits.MaxUserInstances,
		))
		return watchModeDecision{Mode: RuleRuntimeModeWatcher, Reason: strings.Join(reasons, "；")}
	}

	directoryCount, err := detector.WatchDirectoryCount(ctx, rule)
	if cancelErr := terminalContextError(ctx, err); cancelErr != nil {
		return watchModeDecision{
			Reason: "待 watch 目录统计已取消",
			Err:    cancelErr,
		}
	} else if err != nil {
		reasons = append(reasons, fmt.Sprintf("待 watch 目录统计失败，继续尝试 fsnotify：%v", err))
		return watchModeDecision{Mode: RuleRuntimeModeWatcher, Reason: strings.Join(reasons, "；")}
	}
	if limits.CurrentUsageError != nil {
		reasons = append(reasons, fmt.Sprintf("当前 inotify 使用量检测失败，继续按本规则目录数评估：%v", limits.CurrentUsageError))
	}
	totalWatches := limits.CurrentWatches + directoryCount
	if isNearInotifyLimit(totalWatches, limits.MaxUserWatches) {
		return watchModeDecision{
			Mode: RuleRuntimeModePolling,
			Reason: fmt.Sprintf(
				"当前 inotify watch 使用量接近上限（当前 %d + 本规则 %d = %d/%d >= %d%%），使用 polling",
				limits.CurrentWatches,
				directoryCount,
				totalWatches,
				limits.MaxUserWatches,
				watchModeInotifyUsageThresholdNumerator*100/watchModeInotifyUsageThresholdDenominator,
			),
		}
	}
	totalInstances := limits.CurrentInstances + 1
	if isNearInotifyLimit(totalInstances, limits.MaxUserInstances) {
		return watchModeDecision{
			Mode: RuleRuntimeModePolling,
			Reason: fmt.Sprintf(
				"当前 inotify instance 使用量接近上限（当前 %d + 本规则 1 = %d/%d >= %d%%），使用 polling",
				limits.CurrentInstances,
				totalInstances,
				limits.MaxUserInstances,
				watchModeInotifyUsageThresholdNumerator*100/watchModeInotifyUsageThresholdDenominator,
			),
		}
	}

	if filesystemType == "" {
		filesystemType = "unknown"
	}
	reasons = append(reasons, fmt.Sprintf(
		"本地文件系统 %s，当前 watch %d + 本规则 %d = %d/%d，当前 instances %d + 本规则 1 = %d/%d，使用 fsnotify",
		filesystemType,
		limits.CurrentWatches,
		directoryCount,
		totalWatches,
		limits.MaxUserWatches,
		limits.CurrentInstances,
		totalInstances,
		limits.MaxUserInstances,
	))
	return watchModeDecision{Mode: RuleRuntimeModeWatcher, Reason: strings.Join(reasons, "；")}
}

func (detector *osWatchModeDetector) FilesystemType(ctx context.Context, monitorPath string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if detector == nil || detector.goos != "linux" {
		return "", nil
	}
	mountInfoPath := detector.mountInfoPath
	if mountInfoPath == "" {
		mountInfoPath = filepath.Join(detector.procRoot, "self", "mountinfo")
	}
	content, err := os.ReadFile(mountInfoPath)
	if err != nil {
		return "", fmt.Errorf("读取 mountinfo 失败：%w", err)
	}
	filesystemType, ok := filesystemTypeFromMountInfo(monitorPath, string(content))
	if !ok {
		return "", fmt.Errorf("未找到监控目录挂载点：%s", monitorPath)
	}
	return filesystemType, nil
}

func (detector *osWatchModeDetector) InotifyLimits(ctx context.Context) (inotifyLimits, error) {
	if err := ctx.Err(); err != nil {
		return inotifyLimits{}, err
	}
	if detector == nil || detector.goos != "linux" {
		return inotifyLimits{Available: false}, nil
	}
	maxUserWatches, err := detector.readProcInt("sys/fs/inotify/max_user_watches")
	if err != nil {
		return inotifyLimits{}, err
	}
	maxUserInstances, err := detector.readProcInt("sys/fs/inotify/max_user_instances")
	if err != nil {
		return inotifyLimits{}, err
	}
	currentWatches, currentInstances, currentUsageErr := detector.currentInotifyUsage()
	return inotifyLimits{
		Available:         true,
		MaxUserWatches:    maxUserWatches,
		MaxUserInstances:  maxUserInstances,
		CurrentWatches:    currentWatches,
		CurrentInstances:  currentInstances,
		CurrentUsageError: currentUsageErr,
	}, nil
}

func (detector *osWatchModeDetector) WatchDirectoryCount(ctx context.Context, rule *models.DirectoryUploadRule) (int, error) {
	if ctx == nil {
		return 0, errors.New("目录统计上下文为空")
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if rule == nil {
		return 0, errors.New("目录监控规则为空")
	}
	monitorPath := filepath.Clean(rule.MonitorPath)
	info, err := os.Stat(monitorPath)
	if err != nil {
		return 0, err
	}
	if !info.IsDir() {
		return 0, fmt.Errorf("监控路径不是目录：%s", monitorPath)
	}
	if !rule.Recursive {
		return 1, nil
	}

	ignorePatterns := parseIgnorePatterns(rule.IgnorePatternsStr)
	count := 0
	err = filepath.WalkDir(monitorPath, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if !entry.IsDir() {
			return nil
		}
		rel, err := relativePathInMonitor(monitorPath, path)
		if err != nil {
			return err
		}
		if rel != "." && shouldIgnorePath(rel, entry.Name(), true, ignorePatterns) {
			return filepath.SkipDir
		}
		count++
		return nil
	})
	if err != nil {
		return 0, err
	}
	return count, nil
}

func terminalContextError(ctx context.Context, err error) error {
	if ctx != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return nil
}

func (detector *osWatchModeDetector) currentInotifyUsage() (int, int, error) {
	procRoot := "/proc"
	if detector != nil && detector.procRoot != "" {
		procRoot = detector.procRoot
	}
	fdInfoDir := filepath.Join(procRoot, "self", "fdinfo")
	entries, err := os.ReadDir(fdInfoDir)
	if err != nil {
		return 0, 0, fmt.Errorf("读取 fdinfo 失败：%w", err)
	}

	fdDir := filepath.Join(procRoot, "self", "fd")
	currentWatches := 0
	currentInstances := 0
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		fdName := entry.Name()
		content, err := os.ReadFile(filepath.Join(fdInfoDir, fdName))
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return 0, 0, fmt.Errorf("读取 fdinfo %s 失败：%w", fdName, err)
		}

		watchCount := countInotifyWatchesInFDInfo(string(content))
		currentWatches += watchCount
		isInotifyInstance := watchCount > 0
		target, err := os.Readlink(filepath.Join(fdDir, fdName))
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				return 0, 0, fmt.Errorf("读取 fd %s 失败：%w", fdName, err)
			}
		} else if isInotifyAnonInodeTarget(target) {
			isInotifyInstance = true
		}
		if isInotifyInstance {
			currentInstances++
		}
	}
	return currentWatches, currentInstances, nil
}

func countInotifyWatchesInFDInfo(content string) int {
	count := 0
	for line := range strings.SplitSeq(content, "\n") {
		if strings.HasPrefix(line, "inotify wd:") {
			count++
		}
	}
	return count
}

func isInotifyAnonInodeTarget(target string) bool {
	return target == "anon_inode:inotify" || strings.HasPrefix(target, "anon_inode:inotify")
}

func (detector *osWatchModeDetector) readProcInt(path string) (int, error) {
	procRoot := "/proc"
	if detector != nil && detector.procRoot != "" {
		procRoot = detector.procRoot
	}
	content, err := os.ReadFile(filepath.Join(procRoot, path))
	if err != nil {
		return 0, fmt.Errorf("读取 %s 失败：%w", path, err)
	}
	value, err := strconv.Atoi(strings.TrimSpace(string(content)))
	if err != nil {
		return 0, fmt.Errorf("解析 %s 失败：%w", path, err)
	}
	return value, nil
}

func isNetworkOrFuseFilesystem(filesystemType string) bool {
	filesystemType = strings.ToLower(strings.TrimSpace(filesystemType))
	if filesystemType == "" {
		return false
	}
	return strings.Contains(filesystemType, "nfs") ||
		strings.Contains(filesystemType, "cifs") ||
		strings.Contains(filesystemType, "smb") ||
		strings.Contains(filesystemType, "fuse")
}

func isNearInotifyLimit(current int, max int) bool {
	if current <= 0 || max <= 0 {
		return false
	}
	return current*watchModeInotifyUsageThresholdDenominator >= max*watchModeInotifyUsageThresholdNumerator
}

func filesystemTypeFromMountInfo(monitorPath string, mountInfo string) (string, bool) {
	monitorPath = filepath.Clean(monitorPath)
	longestMountPoint := ""
	filesystemType := ""
	for line := range strings.SplitSeq(mountInfo, "\n") {
		fields := strings.Fields(line)
		separatorIndex := -1
		for i, field := range fields {
			if field == "-" {
				separatorIndex = i
				break
			}
		}
		if separatorIndex < 0 || separatorIndex+1 >= len(fields) || len(fields) < 5 {
			continue
		}
		mountPoint := unescapeMountInfoPath(fields[4])
		if !isPathUnderMountPoint(monitorPath, mountPoint) {
			continue
		}
		if len(mountPoint) > len(longestMountPoint) {
			longestMountPoint = mountPoint
			filesystemType = fields[separatorIndex+1]
		}
	}
	return filesystemType, filesystemType != ""
}

func isPathUnderMountPoint(path string, mountPoint string) bool {
	path = filepath.Clean(path)
	mountPoint = filepath.Clean(mountPoint)
	if path == mountPoint {
		return true
	}
	if mountPoint == string(os.PathSeparator) {
		return filepath.IsAbs(path)
	}
	return strings.HasPrefix(path, mountPoint+string(os.PathSeparator))
}

func unescapeMountInfoPath(path string) string {
	replacer := strings.NewReplacer(
		`\\`, `\`,
		`\040`, " ",
		`\011`, "\t",
		`\012`, "\n",
		`\134`, `\`,
	)
	return replacer.Replace(path)
}
