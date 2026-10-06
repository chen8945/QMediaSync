package emby

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"qmediasync/internal/db"
	embyclientrestgo "qmediasync/internal/embyclient-rest-go"
	"qmediasync/internal/models"
)

// embySurvivingItemsError 只包含经 API 实际读到的旧 ID，供匹配屏障安全释放。
type embySurvivingItemsError struct{ IDs []string }

func (e *embySurvivingItemsError) Error() string {
	return "Emby 原条目仍存在，保留原索引及文件"
}

// embyProtectedOwnersError 只来自完整存活清单中明确的新 ID/共享来源证据。
// 它允许再次确认旧 ID 缺失后清旧关联，绝不表示网盘删除成功。
type embyProtectedOwnersError struct{ IDs []string }

func (e *embyProtectedOwnersError) Error() string {
	return "Emby 新条目仍使用原文件，保留网盘并清理已失效的旧关联"
}

func verifyEmbyLocalDeletion(ctx context.Context, input models.EmbyDeletionInput, ownerIDs []string) error {
	config, err := models.ReadEmbyConfigSnapshot()
	if err != nil {
		return err
	}
	if input.ServerID == "" || input.ServerConfigKey != models.EmbyServerConfigIdentity(config) {
		return models.ErrEmbySnapshotStale
	}
	client := embyclientrestgo.NewClient(config.EmbyUrl, config.EmbyApiKey)
	serverID, err := client.GetServerID(ctx)
	if err != nil {
		return err
	}
	if serverID != input.ServerID {
		return models.ErrEmbySnapshotStale
	}
	ids := []string{input.ItemID}
	if len(ownerIDs) == 0 {
		return models.ErrEmbyDeleteUnverified
	}
	for _, id := range ownerIDs {
		if id != input.ItemID && !slices.ContainsFunc(input.Owners, func(owner models.EmbyDeletionOwner) bool { return owner.Item.ItemId == id }) {
			return models.ErrEmbyIdentityAmbiguous
		}
		if !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	items, err := client.GetDeletionVerificationItems(ctx, strings.Join(ids, ","))
	if err != nil {
		return err
	}
	var survivors []string
	for _, item := range items {
		if !slices.Contains(ids, item.Id) {
			return models.ErrEmbyIdentityAmbiguous
		}
		survivors = append(survivors, item.Id)
	}
	if len(survivors) > 0 {
		return &embySurvivingItemsError{IDs: survivors}
	}
	return ctx.Err()
}

const (
	embyDeletionVerificationWindow = 30 * time.Second
	embyDeletionCollectionBudget   = 5 * time.Minute
)

type embyDeletionVerificationKey struct{}

// embyDeletionVerificationState 只存在于一次持续持有 syncscope 的执行中。
// 完整采集单独限时，完成后只短暂复用；分页扫描本身不是原子快照。
type embyDeletionVerificationState struct {
	mu          sync.Mutex
	now         func() time.Time
	completedAt time.Time
	stamp       embyDeletionVerificationStamp
	itemID      string
	loaded      bool
	snapshots   []models.EmbyItemSnapshot
	ledger      map[embyDirectoryAccount]map[string][]models.SyncFile
	sourceReads map[embySourceLocationKey]*embySourceLocationRead
}

type embyDirectoryAccount struct {
	Source models.SourceType
	ID     uint
}

type embyDeletionVerificationStamp struct {
	ServerID        string
	ServerConfigKey string
	ConfigKey       string
	Revision        int64
	EvidenceID      uint
	CurrentConfig   string
	DeletionEnabled int64
}

// newEmbyDeletionVerificationContext 必须在获得 scope 后创建；释放、重取或重领后丢弃。
// 原条目和原 STRM 等批前检查不缓存，旧 policy 也不使用此缓存。
func newEmbyDeletionVerificationContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, embyDeletionVerificationKey{}, &embyDeletionVerificationState{now: time.Now})
}

func invalidateEmbyDeletionVerification(ctx context.Context) {
	if cache, ok := ctx.Value(embyDeletionVerificationKey{}).(*embyDeletionVerificationState); ok {
		cache.mu.Lock()
		cache.loaded = false
		cache.mu.Unlock()
	}
}

func readEmbyDeletionVerificationStamp(ctx context.Context, input models.EmbyDeletionInput) (embyDeletionVerificationStamp, error) {
	var stamp embyDeletionVerificationStamp
	err := db.Db.WithContext(ctx).Model(&models.EmbyIndexState{}).
		Select("server_id, server_config_key, config_key, revision, (SELECT COALESCE(MAX(id), 0) FROM emby_item_evidences) AS evidence_id").
		Where("id = ?", 1).Take(&stamp).Error
	if err != nil {
		return stamp, err
	}
	config, err := models.ReadEmbyConfigSnapshot()
	if err != nil {
		return stamp, err
	}
	if stamp.ServerID != input.ServerID || stamp.ServerConfigKey != input.ServerConfigKey || models.EmbyServerConfigIdentity(config) != input.ServerConfigKey {
		return stamp, models.ErrEmbySnapshotStale
	}
	stamp.CurrentConfig = models.EmbyConfigIdentity(config)
	stamp.DeletionEnabled = int64(config.EnableDeleteNetdisk)
	if config.SyncEnabled != 1 || config.EnableDeleteNetdisk != 1 {
		return stamp, models.ErrEmbyDeleteDisabled
	}
	return stamp, nil
}

func embyDeletionLiveSnapshots(ctx context.Context, input models.EmbyDeletionInput, client *embyclientrestgo.Client) ([]models.EmbyItemSnapshot, error) {
	cache, _ := ctx.Value(embyDeletionVerificationKey{}).(*embyDeletionVerificationState)
	if cache == nil || !input.CleanupPolicy.AllowsJointBatch() {
		if models.IsEmbyDeletionFinalGuard(ctx) {
			return nil, models.ErrEmbySnapshotStale
		}
		return loadEmbyDeletionLiveSnapshots(ctx, client)
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	stamp, err := readEmbyDeletionVerificationStamp(ctx, input)
	if err != nil {
		cache.loaded = false
		return nil, err
	}
	if cache.loaded && cache.stamp == stamp && cache.itemID == input.ItemID && cache.now().Sub(cache.completedAt) < embyDeletionVerificationWindow {
		return cache.snapshots, nil
	}
	if models.IsEmbyDeletionFinalGuard(ctx) {
		cache.loaded = false
		return nil, models.ErrEmbySnapshotStale
	}
	cache.loaded = false
	cache.ledger = nil
	startedAt := cache.now()
	collectionCtx, cancel := context.WithTimeout(ctx, embyDeletionCollectionBudget)
	defer cancel()
	snapshots, err := loadEmbyDeletionLiveSnapshots(collectionCtx, client)
	if err != nil {
		return nil, err
	}
	after, err := readEmbyDeletionVerificationStamp(collectionCtx, input)
	if err != nil {
		return nil, err
	}
	if after != stamp {
		return nil, models.ErrEmbySnapshotStale
	}
	if err := collectionCtx.Err(); err != nil {
		return nil, err
	}
	if cache.now().Sub(startedAt) >= embyDeletionCollectionBudget {
		return nil, context.DeadlineExceeded
	}
	cache.snapshots, cache.stamp, cache.completedAt, cache.itemID = snapshots, stamp, cache.now(), input.ItemID
	cache.loaded = true
	return snapshots, nil
}

func embyDeletionVerificationStillCurrent(ctx context.Context, input models.EmbyDeletionInput) error {
	cache, _ := ctx.Value(embyDeletionVerificationKey{}).(*embyDeletionVerificationState)
	if cache == nil || !input.CleanupPolicy.AllowsJointBatch() {
		return ctx.Err()
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	stamp, err := readEmbyDeletionVerificationStamp(ctx, input)
	if err != nil {
		cache.loaded = false
		return err
	}
	if !cache.loaded || cache.itemID != input.ItemID || cache.stamp != stamp || cache.now().Sub(cache.completedAt) >= embyDeletionVerificationWindow {
		cache.loaded = false
		return models.ErrEmbySnapshotStale
	}
	return ctx.Err()
}

func loadEmbyDeletionLiveSnapshots(ctx context.Context, client *embyclientrestgo.Client) ([]models.EmbyItemSnapshot, error) {
	items, err := client.GetDeletionVerificationItems(ctx, "")
	if err != nil {
		return nil, err
	}
	var snapshots []models.EmbyItemSnapshot
	for _, item := range items {
		if item.Type != "Movie" && item.Type != "Episode" && item.Type != "Video" {
			return nil, models.ErrEmbyIdentityAmbiguous
		}
		members, err := collectEmbySnapshots(ctx, client, item, "", "", "", 0)
		if err != nil {
			return nil, err
		}
		for _, member := range members {
			if member.Item.Path == "" || len(member.Sources) == 0 {
				return nil, errors.New("Emby 存活条目缺少物理来源，无法排除共享文件")
			}
			for _, source := range member.Sources {
				if source.Path == "" {
					return nil, errors.New("Emby 存活来源缺少路径")
				}
			}
		}
		snapshots = append(snapshots, members...)
	}
	return snapshots, nil
}

// verifyEmbyDeletion 复用有界完整清单，但每次仍检查服务、原 ID 与原 STRM。
// 这里不修改索引，也不持有数据库事务；调用者已经获得 syncscope。
func verifyEmbyDeletion(ctx context.Context, input models.EmbyDeletionInput, target models.EmbyDeletionTarget) error {
	config, err := models.ReadEmbyConfigSnapshot()
	if err != nil {
		invalidateEmbyDeletionVerification(ctx)
		return err
	}
	if input.ServerID == "" || input.ServerConfigKey != models.EmbyServerConfigIdentity(config) {
		invalidateEmbyDeletionVerification(ctx)
		return models.ErrEmbySnapshotStale
	}
	client := embyclientrestgo.NewClient(config.EmbyUrl, config.EmbyApiKey)
	serverID, err := client.GetServerID(ctx)
	if err != nil {
		invalidateEmbyDeletionVerification(ctx)
		return err
	}
	if serverID != input.ServerID {
		invalidateEmbyDeletionVerification(ctx)
		return models.ErrEmbySnapshotStale
	}
	ids := []string{input.ItemID}
	var owners []models.EmbyDeletionOwner
	for _, owner := range input.Owners {
		if !slices.ContainsFunc(target.Owners, func(ref models.EmbyDeletionOwnerRef) bool {
			return ref.ItemID == owner.Item.ItemId && ref.SnapshotID == owner.Evidence.ID && ref.Generation == owner.Evidence.Generation
		}) {
			continue
		}
		owners = append(owners, owner)
		if !slices.Contains(ids, owner.Item.ItemId) {
			ids = append(ids, owner.Item.ItemId)
		}
	}
	if len(owners) == 0 {
		return models.ErrEmbyDeleteUnverified
	}
	existing, err := client.GetDeletionVerificationItems(ctx, strings.Join(ids, ","))
	if err != nil {
		invalidateEmbyDeletionVerification(ctx)
		return err
	}
	var surviving []string
	for _, item := range existing {
		if !slices.Contains(ids, item.Id) {
			return models.ErrEmbyIdentityAmbiguous
		}
		surviving = append(surviving, item.Id)
	}
	if len(surviving) > 0 {
		invalidateEmbyDeletionVerification(ctx)
		return &embySurvivingItemsError{IDs: surviving}
	}
	for _, owner := range owners {
		if len(owner.Files) == 0 {
			return models.ErrEmbyDeleteUnverified
		}
		for _, file := range owner.Files {
			if file.Reason != "" || owner.Item.Path != file.LocalFilePath {
				return models.ErrEmbyIdentityAmbiguous
			}
		}
	}
	snapshots, err := embyDeletionLiveSnapshots(ctx, input, client)
	if err != nil {
		return err
	}
	var protected []string
	for _, snapshot := range snapshots {
		for _, owner := range owners {
			if snapshot.Item.Path == owner.Item.Path || snapshot.Item.ItemId == owner.Item.ItemId {
				if !slices.Contains(protected, owner.Item.ItemId) {
					protected = append(protected, owner.Item.ItemId)
				}
			}
			var historical []models.EmbySnapshotSource
			if err := json.Unmarshal([]byte(owner.Evidence.SourcesJSON), &historical); err != nil {
				return err
			}
			for _, source := range snapshot.Sources {
				if source.Path == "" {
					return errors.New("Emby 存活来源缺少路径")
				}
				for _, file := range owner.Files {
					if source.ID != "" && source.ID == file.SourceID ||
						sameEmbySource(source.PickCode, file.PickCode) ||
						sameEmbySource(source.Path, filepath.Join(file.Path, file.FileName)) {
						if !slices.Contains(protected, owner.Item.ItemId) {
							protected = append(protected, owner.Item.ItemId)
						}
					}
				}
				for _, old := range historical {
					if sameEmbySource(source.Path, old.Path) {
						if !slices.Contains(protected, owner.Item.ItemId) {
							protected = append(protected, owner.Item.ItemId)
						}
					}
				}
			}
		}
	}
	if len(protected) > 0 {
		return &embyProtectedOwnersError{IDs: protected}
	}
	scopedFiles := target.ScopedMetadata
	if len(scopedFiles) == 0 && target.Kind == "scoped_metadata" {
		scopedFiles = []models.EmbyFrozenFile{target.File}
	}
	if len(scopedFiles) > 0 {
		if input.ItemType != "Season" && input.ItemType != "Series" || !input.CleanupPolicy.AllowsDirectoryContent() {
			return models.ErrEmbyDeleteUnverified
		}
		var protectedKeys []string
		for _, file := range scopedFiles {
			var seasons []string
			for _, scoped := range input.ScopedFiles {
				if scoped.File == file && scoped.ScopeType == "Season" && scoped.ScopeItemID != "" && (input.ItemType == "Series" || scoped.ScopeItemID == input.ItemID) {
					seasons = append(seasons, scoped.ScopeItemID)
				}
			}
			if len(seasons) == 0 {
				return models.ErrEmbyDeleteUnverified
			}
			if slices.ContainsFunc(snapshots, func(snapshot models.EmbyItemSnapshot) bool { return slices.Contains(seasons, snapshot.Item.SeasonId) }) {
				protectedKeys = append(protectedKeys, models.EmbyDeletionFileKey(file))
			}
		}
		if len(protectedKeys) > 0 {
			return &models.EmbyDeletionTargetConflict{Keys: protectedKeys, Err: errors.New("Emby 本季仍有存活成员，保留季级元数据")}
		}
	}
	if target.Directory != nil {
		if !input.CleanupPolicy.AllowsDirectoryContent() {
			return models.ErrEmbyDeleteUnverified
		}
		if err := verifyEmbyDirectoryLiveItems(ctx, *target.Directory, snapshots); err != nil {
			return err
		}
	}
	for _, owner := range owners {
		for _, file := range owner.Files {
			if err := embyOriginalPathAbsent(file.LocalRoot, owner.Item.Path); err != nil {
				invalidateEmbyDeletionVerification(ctx)
				return err
			}
		}
	}

	return embyDeletionVerificationStillCurrent(ctx, input)
}

// verifyEmbyDirectoryLiveItems 将直接子项身份与存活来源交叉核验，防止账本旧路径漏掉外部移入。
// 不递归子目录，不查询陌生文件详情；必要时只反查明确的存活来源。
func verifyEmbyDirectoryLiveItems(ctx context.Context, scope models.EmbyDirectoryScope, snapshots []models.EmbyItemSnapshot) error {
	if !filepath.IsAbs(scope.LocalPath) || scope.Root.AccountID == 0 || scope.Root.SourceType == "" {
		return models.ErrEmbyIdentityAmbiguous
	}
	children, err := models.ListEmbyDeletionDirectory(ctx, scope)
	if err != nil {
		return err
	}
	physicalIDs, sourceCodes := map[string]bool{}, map[string]bool{}
	for _, child := range children {
		if child.IsDir {
			continue
		}
		if child.FileID != "" {
			physicalIDs[child.FileID] = true
		}
		code := child.PickCode
		if scope.Root.SourceType == models.SourceTypeBaiduPan {
			code = child.FileID
		}
		if code != "" {
			sourceCodes[canonicalEmbySource(code)] = true
		}
	}
	var files map[string][]models.SyncFile
	loaded := false
	var sourceReads []embySourceLocationUse
	remoteRoot := path.Join("/", scope.Root.Path, scope.Root.FileName)
	for _, snapshot := range snapshots {
		if !filepath.IsAbs(snapshot.Item.Path) {
			return errors.New("Emby 存活条目的目录位置无法核验")
		}
		if embyVerificationPathWithin(scope.LocalPath, snapshot.Item.Path) {
			return errors.New("Emby 存活条目仍位于待删除媒体目录")
		}
		for _, source := range snapshot.Sources {
			if source.PickCode == "" && filepath.IsAbs(source.Path) {
				// 本地物理媒体源可以直接核对；远端命名空间不能用本地目录排除。
				if embyVerificationPathWithin(scope.LocalPath, source.Path) || embyVerificationPathWithin(remoteRoot, source.Path) {
					return errors.New("Emby 存活来源仍位于待删除媒体目录")
				}
				continue
			}
			if source.PickCode != "" && !loaded {
				var err error
				files, err = embyDirectorySourceLedger(ctx, scope.Root)
				if err != nil {
					return err
				}
				loaded = true
			}
			matches := files[canonicalEmbySource(source.PickCode)]
			for _, file := range matches {
				physicalID := file.FileId
				if scope.Root.SourceType == models.SourceTypeBaiduPan {
					// 百度账本 FileId 是历史路径，PickCode 才是稳定 fsid。
					physicalID = file.PickCode
				}
				if physicalID != "" && physicalIDs[physicalID] {
					return errors.New("Emby 存活来源仍使用待删除目录中的文件")
				}
				if file.Path == "" || file.FileName == "" {
					return errors.New("Emby 存活来源的记录位置不完整")
				}
				if embyVerificationPathWithin(remoteRoot, path.Join("/", file.Path, file.FileName)) {
					return errors.New("Emby 存活来源仍使用待删除目录中的文件")
				}
			}
			if sourceCodes[canonicalEmbySource(source.PickCode)] {
				// 直接子项已经提供稳定身份，无需再请求该存活来源的详情。
				return errors.New("Emby 存活来源仍使用待删除目录中的文件")
			}
			if len(matches) == 0 {
				read, err := embyDirectorySourceLocation(ctx, scope.Root, source)
				if err != nil {
					return errors.New("Emby 存活来源的远端位置无法核验，保留媒体目录")
				}
				if embyVerificationPathWithin(remoteRoot, read.path) {
					return errors.New("Emby 存活来源仍使用待删除目录中的文件")
				}
				sourceReads = append(sourceReads, embySourceLocationUse{read: read, source: source})
			}
		}
	}
	return verifyEmbySourceLocationReads(ctx, scope.Root, sourceReads)
}

func embyDirectorySourceLedger(ctx context.Context, root models.EmbyFrozenFile) (map[string][]models.SyncFile, error) {
	cache, _ := ctx.Value(embyDeletionVerificationKey{}).(*embyDeletionVerificationState)
	key := embyDirectoryAccount{Source: root.SourceType, ID: root.AccountID}
	if cache != nil {
		cache.mu.Lock()
		defer cache.mu.Unlock()
		if files, ok := cache.ledger[key]; ok && cache.loaded {
			return files, nil
		}
	}
	var files []models.SyncFile
	if err := db.Db.WithContext(ctx).Model(&models.SyncFile{}).
		Select("file_id, pick_code, path, file_name").Where("source_type = ? AND account_id = ? AND pick_code <> ''", root.SourceType, root.AccountID).
		Find(&files).Error; err != nil {
		return nil, err
	}
	bySource := map[string][]models.SyncFile{}
	for _, file := range files {
		key := canonicalEmbySource(file.PickCode)
		bySource[key] = append(bySource[key], file)
	}
	if cache != nil && cache.loaded {
		if cache.ledger == nil {
			cache.ledger = map[embyDirectoryAccount]map[string][]models.SyncFile{}
		}
		cache.ledger[key] = bySource
	}
	return bySource, nil
}

func embyVerificationPathWithin(root, candidate string) bool {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(candidate))
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

// embyOriginalPathAbsent 必须先证明挂载根仍可访问；挂载丢失不等于原文件删除。
func embyOriginalPathAbsent(root, original string) error {
	if !filepath.IsAbs(root) || !filepath.IsAbs(original) {
		return models.ErrEmbyDeleteUnverified
	}
	rel, err := filepath.Rel(root, original)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return models.ErrEmbyDeleteUnverified
	}
	rootInfo, err := os.Stat(root)
	if err != nil || !rootInfo.IsDir() {
		return errors.New("原 STRM 挂载根不可访问")
	}
	// Lstat 使断链也被视为仍有原件；中间符号链接不作为缺失证明。
	current := filepath.Clean(root)
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return errors.New("原 STRM 路径无法核验")
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("原 STRM 路径仍存在符号链接")
		}
	}
	return fmt.Errorf("%w: 原 STRM 路径仍存在", models.ErrEmbyDeleteUnverified)
}

func sameEmbySource(left, right string) bool {
	return left != "" && right != "" && canonicalEmbySource(left) == canonicalEmbySource(right)
}

func canonicalEmbySource(value string) string {
	u, err := url.Parse(value)
	if err != nil || u.Scheme != "http" && u.Scheme != "https" {
		return value
	}
	u.User, u.Fragment = nil, ""
	query := url.Values{}
	for _, key := range []string{"pickcode", "pick_code"} {
		if code := u.Query().Get(key); code != "" {
			query.Set(key, code)
		}
	}
	u.RawQuery = query.Encode()
	return u.String()
}
