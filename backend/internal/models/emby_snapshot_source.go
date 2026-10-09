package models

import (
	"context"
	"errors"
	"net/url"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"qmediasync/internal/db"
)

// EmbySTRMReference 冻结 Emby 实际引用；不替代 QMS 管理路径。
// SyncPathID 表示已绑定文件或用户声明的结构归属；AutoSyncPathID 只证明本地定位根。
type EmbySTRMReference struct {
	EmbyPath         string `json:"emby_path"`
	EmbyRoot         string `json:"emby_root,omitempty"`
	LocalPath        string `json:"local_path,omitempty"`
	LocalRoot        string `json:"local_root,omitempty"`
	SyncPathID       uint   `json:"sync_path_id,omitempty"`
	AutoSyncPathID   uint   `json:"auto_sync_path_id,omitempty"`
	AutoRootIdentity string `json:"auto_root_identity,omitempty"`
	Reason           string `json:"reason,omitempty"`
}

// ResolveEmbySTRMReference 只使用明确的配置映射，不将文件父目录或 / 当成可访问根。
func ResolveEmbySTRMReference(config *EmbyConfig, embyPath string) EmbySTRMReference {
	reference := EmbySTRMReference{EmbyPath: sanitizeEmbyEvidencePath(embyPath)}
	if config == nil {
		return reference
	}
	for _, mapping := range config.PathMappings {
		if _, _, err := normalizeEmbyMappingPath(mapping.EmbyRoot); err != nil {
			return EmbySTRMReference{EmbyPath: reference.EmbyPath, Reason: "invalid_path_mapping"}
		}
		if _, matches := mapping.RelativePath(embyPath); !matches {
			continue
		}
		local, ok := mapping.ResolveLocalPath(embyPath)
		if !ok {
			return EmbySTRMReference{EmbyPath: reference.EmbyPath, Reason: "invalid_path_mapping"}
		}
		if reference.LocalRoot != "" {
			return EmbySTRMReference{EmbyPath: reference.EmbyPath, Reason: "ambiguous_path_mapping"}
		}
		reference.EmbyRoot, reference.LocalRoot = mapping.EmbyRoot, mapping.LocalRoot
		reference.LocalPath, reference.SyncPathID = local, mapping.SyncPathID
	}
	return reference
}

// ValidateEmbySTRMReference 拒绝映射变更后继续使用旧的实际引用范围。
func ValidateEmbySTRMReference(ctx context.Context, config *EmbyConfig, reference EmbySTRMReference) error {
	conn := db.Db
	if conn != nil {
		conn = conn.WithContext(ctx)
	}
	return validateEmbySTRMReferenceTx(conn, config, reference)
}

func validateEmbySTRMReferenceTx(tx *gorm.DB, config *EmbyConfig, reference EmbySTRMReference) error {
	if reference.EmbyPath == "" || reference.Reason != "" || reference.LocalRoot == "" || reference.LocalPath == "" {
		return ErrEmbyDeleteUnverified
	}
	if reference.AutoSyncPathID != 0 {
		if tx == nil {
			return ErrEmbyDeleteUnverified
		}
		var root SyncPath
		if err := tx.First(&root, reference.AutoSyncPathID).Error; err != nil {
			return err
		}
		var account Account
		if err := tx.First(&account, root.AccountId).Error; err != nil {
			return err
		}
		if root.SourceType != account.SourceType {
			return ErrEmbySnapshotStale
		}
		return validateEmbyAutoSTRMReference(config, reference, root, EmbyAccountIdentity(account))
	}
	if ResolveEmbySTRMReference(config, reference.EmbyPath) != reference {
		return ErrEmbySnapshotStale
	}
	return nil
}

func embyAutoSTRMReference(embyPath, localPath string, root SyncPath, accountIdentity string) EmbySTRMReference {
	reference := EmbySTRMReference{EmbyPath: sanitizeEmbyEvidencePath(embyPath)}
	mapping, ok := inferEmbyPathMapping(embyPath, root.GetFullLocalPath(), localPath)
	if !ok || root.ID == 0 || accountIdentity == "" || !strings.EqualFold(filepath.Ext(localPath), ".strm") {
		return reference
	}
	reference.EmbyRoot, reference.LocalRoot = mapping.EmbyRoot, mapping.LocalRoot
	reference.LocalPath, reference.AutoSyncPathID = localPath, root.ID
	reference.AutoRootIdentity = embyDigest([]any{
		root.ID, root.CreatedAt, root.SourceType, root.AccountId, accountIdentity,
		root.GetFullLocalPath(), root.RemotePath, root.BaseCid,
	})
	return reference
}

func validateEmbyAutoSTRMReference(config *EmbyConfig, reference EmbySTRMReference, root SyncPath, accountIdentity string) error {
	explicit := ResolveEmbySTRMReference(config, reference.EmbyPath)
	if explicit.LocalRoot != "" || explicit.Reason != "" ||
		reference.SyncPathID != 0 && reference.SyncPathID != root.ID {
		return ErrEmbySnapshotStale
	}
	expected := embyAutoSTRMReference(reference.EmbyPath, reference.LocalPath, root, accountIdentity)
	expected.SyncPathID = reference.SyncPathID
	if expected.AutoSyncPathID == 0 || expected != reference {
		return ErrEmbySnapshotStale
	}
	return nil
}

// EmbySourceLookupIdentity 从本实例支持的播放路由提取账号和对象，不读取播放 URL。
// 与原目录反查使用同一纯解析器，URL 中 path/truepath 不参与对象定位。
func EmbySourceLookupIdentity(source EmbySnapshotSource, bases []string) (SourceType, string, string, bool) {
	u, query, ok := embySourceURL(source.Path)
	if !ok || u.RawPath != "" || len(query["userid"]) != 1 || query.Get("userid") == "" {
		return "", "", "", false
	}
	code := ""
	for _, key := range []string{"pickcode", "pick_code"} {
		values := query[key]
		if len(values) > 1 || len(values) == 1 && (values[0] == "" || code != "" && code != values[0]) {
			return "", "", "", false
		}
		if len(values) == 1 {
			code = values[0]
		}
	}
	if len(query["pickcode"]) != 1 || !validEmbySnapshotCode(code) || source.PickCode != "" && source.PickCode != code {
		return "", "", "", false
	}
	for _, base := range bases {
		configured, err := url.Parse(base)
		if err != nil || configured.User != nil || configured.Fragment != "" || configured.Scheme != u.Scheme || !strings.EqualFold(configured.Host, u.Host) {
			continue
		}
		routes := []string{u.Path}
		prefix := strings.TrimRight(configured.Path, "/")
		if configured.RawPath == "" && prefix != "" && validEmbySnapshotURLPath(prefix) {
			if route, matched := strings.CutPrefix(u.Path, prefix); matched {
				routes = append(routes, route)
			}
		}
		for _, route := range routes {
			sourceType := embySnapshotRouteType(route)
			if sourceType == "" {
				continue
			}
			if sourceType == SourceTypeBaiduPan {
				id, err := strconv.ParseInt(code, 10, 64)
				if err != nil || id <= 0 || strconv.FormatInt(id, 10) != code {
					return "", "", "", false
				}
			}
			return sourceType, query.Get("userid"), code, true
		}
	}
	return "", "", "", false
}

// embySourceAccountHint 只缩小现有账本候选，不为未绑定 URL 授予账号或删除权限。
// 保留路由前缀，让代理路径里的显式账号声明也能阻止同码跨账号误绑。
func embySourceAccountHint(source EmbySnapshotSource) (SourceType, string, string, bool) {
	u, _, ok := embySourceURL(source.Path)
	if !ok {
		return "", "", "", false
	}
	origin := u.Scheme + "://" + u.Host
	bases := []string{origin}
	for _, marker := range []string{"/115/", "/baidupan/"} {
		if index := strings.LastIndex(u.Path, marker); index > 0 {
			bases = append(bases, origin+u.Path[:index])
		}
	}
	return EmbySourceLookupIdentity(source, bases)
}

func embySourceURL(raw string) (*url.URL, url.Values, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.Opaque != "" || u.Fragment != "" || u.Scheme != "http" && u.Scheme != "https" || !validEmbySnapshotURLPath(u.Path) {
		return nil, nil, false
	}
	query, err := url.ParseQuery(u.RawQuery)
	return u, query, err == nil
}

func validEmbySnapshotCode(code string) bool {
	if code == "" || len(code) > 128 {
		return false
	}
	for _, c := range code {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

func validEmbySnapshotURLPath(value string) bool {
	return strings.HasPrefix(value, "/") && path.Clean(value) == value && !strings.ContainsAny(value, "\\\x00")
}

func embySnapshotRouteType(route string) SourceType {
	switch {
	case route == "/115/newurl" || strings.HasPrefix(route, "/115/url/") && len(route) > len("/115/url/"):
		return SourceType115
	case strings.HasPrefix(route, "/baidupan/url/") && len(route) > len("/baidupan/url/"):
		return SourceTypeBaiduPan
	}
	return ""
}

func embySnapshotSourceBases(tx *gorm.DB) ([]string, error) {
	cache := embyApplyCacheFrom(tx)
	if cache != nil {
		if cache.sourceBasesLoaded {
			cache.sourceBaseUses++
			return cache.sourceBases, nil
		}
		tx = tx.Clauses(clause.Locking{Strength: "SHARE"}).Session(&gorm.Session{})
	}
	var settings Settings
	if err := tx.Select("strm_base_url").First(&settings).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	bases := []string{settings.StrmBaseUrl}
	var paths []SyncPath
	if err := tx.Select("strm_base_url").Where("strm_base_url <> ''").Find(&paths).Error; err != nil {
		return nil, err
	}
	for _, root := range paths {
		bases = append(bases, root.StrmBaseUrl)
	}
	if cache != nil {
		cache.sourceBases, cache.sourceBasesLoaded = bases, true
		cache.sourceBaseUses = 1
		cache.validations = append(cache.validations, func() error {
			if cache.sourceBaseUses < 2 {
				return nil
			}
			current, err := embySnapshotSourceBases(tx.Set(embyApplyCacheKey, nil).Session(&gorm.Session{}))
			if err != nil {
				return err
			}
			left, right := append([]string(nil), bases...), append([]string(nil), current...)
			slices.Sort(left)
			slices.Sort(right)
			if !slices.Equal(left, right) {
				return ErrEmbySnapshotStale
			}
			return nil
		})
	}
	return bases, nil
}

// ResolveEmbySnapshotSourceIdentity 为删除时的存活来源核验复用本地身份解析，不访问网盘。
func ResolveEmbySnapshotSourceIdentity(ctx context.Context, source EmbySnapshotSource) (EmbySnapshotSource, error) {
	return resolveEmbySourceIdentity(db.Db.WithContext(ctx), source)
}

// resolveEmbySourceIdentity 只查询可定位的本地账号，不遍历账号进行远端试探。
func resolveEmbySourceIdentity(tx *gorm.DB, source EmbySnapshotSource) (EmbySnapshotSource, error) {
	// 传入快照来自 Emby；不能把调用方携带的结构化身份当成已验证账号。
	source.SourceType, source.AccountID, source.AccountIdentity, source.ObjectKind, source.ObjectID, source.SyncFileID = "", 0, "", "", "", 0
	if filepath.IsAbs(source.Path) && filepath.Clean(source.Path) == source.Path {
		source.SourceType, source.ObjectKind, source.ObjectID = SourceTypeLocal, "local_path", source.Path
		return source, nil
	}
	u, query, ok := embySourceURL(source.Path)
	if !ok {
		return source, nil
	}
	// 先检查路由形状，普通或未知 URL 不触发设置/账号查找。
	if _, _, _, hasAccountHint := embySourceAccountHint(source); hasAccountHint {
		bases, err := embySnapshotSourceBases(tx)
		if err != nil {
			return source, err
		}
		sourceType, userID, objectID, recognized := EmbySourceLookupIdentity(source, bases)
		if recognized {
			source.SourceType, source.ObjectID, source.ObjectKind = sourceType, objectID, "pickcode"
			if sourceType == SourceTypeBaiduPan {
				source.ObjectKind = "fs_id"
			}
			accounts, err := loadEmbyApplySourceAccounts(tx, sourceType, userID)
			if err != nil {
				return source, err
			}
			if len(accounts) == 1 {
				source.AccountID, source.AccountIdentity = accounts[0].ID, EmbyAccountIdentity(accounts[0])
			}
			return source, nil
		}
	}
	// OpenList 的 /d/ 路由只包含路径；签名不是稳定对象身份。
	prefix, remote, found := strings.Cut(u.Path, "/d/")
	if !found || remote == "" || len(query["sign"]) > 1 {
		return source, nil
	}
	base := u.Scheme + "://" + u.Host + prefix
	var accounts []Account
	if err := tx.Where("source_type = ? AND (base_url = ? OR base_url = ?)", SourceTypeOpenList, base, base+"/").Limit(2).Find(&accounts).Error; err != nil {
		return source, err
	}
	source.SourceType, source.ObjectKind, source.ObjectID = SourceTypeOpenList, "openlist_path", "/"+remote
	if len(accounts) == 1 {
		source.AccountID, source.AccountIdentity = accounts[0].ID, EmbyAccountIdentity(accounts[0])
	}
	return source, nil
}

func freezeEmbySnapshotSources(tx *gorm.DB, snapshot EmbyItemSnapshot, files []EmbyFrozenFile) ([]EmbySnapshotSource, error) {
	sources := append([]EmbySnapshotSource(nil), snapshot.Sources...)
	config := snapshot.pathConfig
	for i := range sources {
		source := &sources[i]
		var bound *EmbyFrozenFile
		uniqueSourceID := source.ID != ""
		for j, other := range sources {
			if i != j && other.ID == source.ID {
				uniqueSourceID = false
				break
			}
		}
		// 重复 SourceID 仍按对象和账号核对，不能串用另一个来源的绑定。
		for j := range files {
			file := &files[j]
			if uniqueSourceID && file.SourceID == source.ID {
				if bound != nil && bound.SyncFileID != file.SyncFileID {
					return nil, ErrEmbyIdentityAmbiguous
				}
				bound = file
			}
		}
		if bound == nil {
			matches := []*EmbyFrozenFile{}
			for j := range files {
				file := &files[j]
				if source.PickCode != "" && sanitizeEmbyEvidencePath(source.PickCode) == file.PickCode || source.Path == file.FileID && file.SourceType == SourceTypeLocal {
					matches = append(matches, file)
				}
			}
			if sourceType, userID, _, recognized := embySourceAccountHint(*source); recognized && len(matches) > 0 {
				accounts, err := loadEmbyApplySourceAccounts(tx, sourceType, userID)
				if err != nil {
					return nil, err
				}
				matches = slices.DeleteFunc(matches, func(file *EmbyFrozenFile) bool {
					return len(accounts) != 1 || file.SourceType != sourceType || file.AccountID != accounts[0].ID || file.AccountIdentity != EmbyAccountIdentity(accounts[0])
				})
			}
			if len(matches) > 1 {
				return nil, ErrEmbyIdentityAmbiguous
			}
			if len(matches) == 1 {
				bound = matches[0]
			}
		}
		if bound != nil {
			if _, wasUnbound := snapshot.preparedSTRMReferences[i]; wasUnbound {
				return nil, ErrEmbySnapshotStale
			}
			source.SourceType, source.AccountID, source.AccountIdentity, source.SyncFileID = bound.SourceType, bound.AccountID, bound.AccountIdentity, bound.SyncFileID
			source.ObjectKind, source.ObjectID = "file_id", bound.FileID
			switch bound.SourceType {
			case SourceTypeBaiduPan:
				source.ObjectKind, source.ObjectID = "fs_id", bound.PickCode
			case SourceTypeOpenList:
				source.ObjectKind, source.ObjectID = "openlist_id", bound.OpenlistObjectID
				if source.ObjectID == "" {
					source.ObjectKind, source.ObjectID = "openlist_path", bound.FileID
				}
			case SourceTypeLocal:
				source.ObjectKind = "local_path"
			}
			source.Reference = bound.Reference
			if source.Reference.EmbyPath == "" {
				source.Reference = EmbySTRMReference{EmbyPath: sanitizeEmbyEvidencePath(snapshot.Item.Path), EmbyRoot: bound.LocalRoot, LocalPath: bound.LocalFilePath, LocalRoot: bound.LocalRoot, SyncPathID: bound.SyncPathID}
			}
			continue
		}
		resolved, err := resolveEmbySourceIdentity(tx, *source)
		if err != nil {
			return nil, err
		}
		*source = resolved
		if config == nil {
			config = &EmbyConfig{}
			if err := tx.First(config).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, err
			}
		}
		source.Reference = ResolveEmbySTRMReference(config, snapshot.Item.Path)
		if prepared, ok := snapshot.preparedSTRMReferences[i]; ok {
			if prepared.reference.EmbyPath != sanitizeEmbyEvidencePath(snapshot.Item.Path) {
				return nil, ErrEmbySnapshotStale
			}
			if err := validatePreparedEmbySTRMReference(tx, config, *source, prepared); err != nil {
				return nil, err
			}
			source.Reference = prepared.reference
		}
	}
	return sources, nil
}

func embySnapshotIdentity(item EmbyMediaItem, files []EmbyFrozenFile, sources []EmbySnapshotSource) string {
	identity := []any{item.ItemId, item.Path, item.DateCreated, files}
	unbound := []EmbySnapshotSource{}
	for _, source := range sources {
		if source.SyncFileID == 0 {
			source.Path, source.PickCode = sanitizeEmbyEvidencePath(source.Path), sanitizeEmbyEvidencePath(source.PickCode)
			unbound = append(unbound, source)
		}
	}
	if len(unbound) != 0 {
		slices.SortFunc(unbound, func(a, b EmbySnapshotSource) int { return strings.Compare(embyJSON(a), embyJSON(b)) })
		identity = append(identity, unbound)
	}
	return embyDigest(identity)
}
