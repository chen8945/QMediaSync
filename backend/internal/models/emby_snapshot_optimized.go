package models

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"reflect"
	"slices"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"qmediasync/internal/db"
)

var errEmbyApplyNeedsExclusive = errors.New("Emby 旧成员需要排他提交")

const embyApplyCacheKey = "qms:emby-apply-cache"

type embyApplyMembershipRow struct {
	EmbyItemMembership
	HasNull bool
}

type embyApplyCache struct {
	memberships        map[string]embyApplyMembershipRow
	config             *EmbyConfig
	media              map[string]EmbyMediaItem
	states             map[string]EmbyItemState
	evidence           map[uint]EmbyItemEvidence
	files              map[uint]SyncFile
	picks              map[string][]SyncFile
	roots              map[uint]SyncPath
	rootLoaded         map[uint]bool
	accounts           map[uint]Account
	accountLoaded      map[uint]bool
	links              map[uint][]EmbyMediaSyncFile
	candidates         map[string][]SyncFile
	sourceAccounts     map[string][]Account
	sourceBases        []string
	sourceBasesLoaded  bool
	rootEvidence       map[uint]SyncRootEvidence
	rootEvidenceLoaded map[uint]bool
	validations        []func() error
	candidateUses      map[string]int
	sourceAccountUses  map[string]int
	pickUses           map[string]int
	sourceBaseUses     int
}

func embyApplyCacheFrom(tx *gorm.DB) *embyApplyCache {
	value, ok := tx.Get(embyApplyCacheKey)
	if !ok {
		return nil
	}
	cache, _ := value.(*embyApplyCache)
	return cache
}

func newEmbyApplyCache(config *EmbyConfig) *embyApplyCache {
	return &embyApplyCache{
		config:             config,
		media:              map[string]EmbyMediaItem{},
		states:             map[string]EmbyItemState{},
		evidence:           map[uint]EmbyItemEvidence{},
		files:              map[uint]SyncFile{},
		picks:              map[string][]SyncFile{},
		roots:              map[uint]SyncPath{},
		rootLoaded:         map[uint]bool{},
		accounts:           map[uint]Account{},
		accountLoaded:      map[uint]bool{},
		links:              map[uint][]EmbyMediaSyncFile{},
		candidates:         map[string][]SyncFile{},
		sourceAccounts:     map[string][]Account{},
		rootEvidence:       map[uint]SyncRootEvidence{},
		rootEvidenceLoaded: map[uint]bool{},
		candidateUses:      map[string]int{},
		sourceAccountUses:  map[string]int{},
		pickUses:           map[string]int{},
	}
}

// lockEmbyApplyGuard 不改动通用 checkEmbyToken，普通提交不能升级共享锁。
func lockEmbyApplyGuard(tx *gorm.DB, token EmbyIndexToken, exclusive bool) (*EmbyConfig, error) {
	var state EmbyIndexState
	if tx.Dialector.Name() == "postgres" {
		strength := "SHARE"
		if exclusive {
			strength = "UPDATE"
		}
		if err := tx.Clauses(clause.Locking{Strength: strength}).First(&state, 1).Error; err != nil {
			return nil, err
		}
	} else {
		// Begin 已创建 state；RETURNING 同时取得 writer 保护和状态，省去冲突插入及随后 SELECT。
		result := tx.Model(&state).Clauses(clause.Returning{}).Where("id = ?", 1).UpdateColumn("revision", gorm.Expr("revision + 0"))
		if result.Error != nil {
			return nil, result.Error
		}
		if result.RowsAffected != 1 {
			return nil, ErrEmbySnapshotStale
		}
	}
	if state.ServerID != token.ServerID || state.ConfigKey != token.ConfigKey || state.ServerConfigKey != token.ServerConfigKey || state.Revision != token.Revision {
		return nil, ErrEmbySnapshotStale
	}
	var config EmbyConfig
	if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).First(&config).Error; err != nil {
		return nil, err
	}
	if config.SyncEnabled != 1 || EmbyConfigIdentity(&config) != token.ConfigKey {
		return nil, ErrEmbySnapshotStale
	}
	return &config, nil
}

// LoadEmbySnapshotSerialItems 一次读取旧成员边，供执行器保留冲突组的枚举顺序。
func LoadEmbySnapshotSerialItems(ctx context.Context, token EmbyIndexToken) (map[string]bool, error) {
	ids := map[string]bool{}
	err := db.Db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if _, err := lockEmbyApplyGuard(tx, token, false); err != nil {
			return err
		}
		var rows []EmbyMediaItem
		if err := tx.Select("item_id", "part_of_item_id", "version_of_item_id").Where("part_of_item_id <> '' OR version_of_item_id <> ''").Find(&rows).Error; err != nil {
			return err
		}
		for _, row := range rows {
			ids[row.ItemId] = true
			if row.PartOfItemID != "" {
				ids[row.PartOfItemID] = true
			}
			if row.VersionOfItemID != "" {
				ids[row.VersionOfItemID] = true
			}
		}
		return nil
	})
	return ids, err
}

func lockEmbyApplyItems(tx *gorm.DB, snapshots []EmbyItemSnapshot) error {
	if tx.Dialector.Name() != "postgres" {
		return nil
	}
	keys := make([]int64, 0, len(snapshots))
	for _, snapshot := range snapshots {
		// 媒体 item_id 是全局唯一；锁不依赖尚未存在的 state 行，也不按服务器拆开。
		sum := sha256.Sum256([]byte("qms:emby:item:" + snapshot.Item.ItemId))
		keys = append(keys, int64(binary.BigEndian.Uint64(sum[:8])))
	}
	slices.Sort(keys)
	for _, key := range slices.Compact(keys) {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(?)", key).Error; err != nil {
			return err
		}
	}
	return nil
}

func loadEmbyApplyItems(tx *gorm.DB, token EmbyIndexToken, snapshots []EmbyItemSnapshot, cache *embyApplyCache) error {
	ids := make([]string, 0, len(snapshots))
	intIDs := make([]int64, 0, len(snapshots))
	for _, snapshot := range snapshots {
		id, err := parseEmbyItemID(snapshot.Item.ItemId)
		if err != nil {
			return err
		}
		ids = append(ids, snapshot.Item.ItemId)
		intIDs = append(intIDs, id)
	}
	if len(ids) == 0 {
		return nil
	}
	if tx.Dialector.Name() == "sqlite" {
		// 多 ID 的成员查询拆成等值条件，避免 ORDER BY 选择全服务器扫描。
		queries := make([]*gorm.DB, 0, 3*len(ids))
		for _, id := range ids {
			queries = append(queries,
				tx.Where("item_id = ?", id),
				tx.Where("server_id = ? AND part_of_item_id = ?", token.ServerID, id),
				tx.Where("server_id = ? AND version_of_item_id = ?", token.ServerID, id))
		}
		for _, query := range queries {
			var media []EmbyMediaItem
			if err := query.Order("id").Find(&media).Error; err != nil {
				return err
			}
			for _, row := range media {
				cache.media[row.ItemId] = row
			}
		}
	} else {
		var media []EmbyMediaItem
		if err := tx.Where("item_id IN ? OR (server_id = ? AND (part_of_item_id IN ? OR version_of_item_id IN ?))", ids, token.ServerID, ids, ids).Order("id").Find(&media).Error; err != nil {
			return err
		}
		for _, row := range media {
			cache.media[row.ItemId] = row
		}
	}
	if tx.Dialector.Name() == "sqlite" || tx.Dialector.Name() == "postgres" {
		// 预读位于索引保护和条目锁内；NULL 记录仍必须修复，不能当作相同投影。
		var memberships []embyApplyMembershipRow
		if err := tx.Model(&EmbyItemMembership{}).
			Select("emby_item_memberships.*, (server_config_key IS NULL OR snapshot_id IS NULL OR item_type IS NULL OR part_of_item_id IS NULL OR season_id IS NULL OR series_id IS NULL) AS has_null").
			Where("server_id = ? AND item_id IN ?", token.ServerID, ids).Find(&memberships).Error; err != nil {
			return err
		}
		cache.memberships = make(map[string]embyApplyMembershipRow, len(memberships))
		for _, row := range memberships {
			cache.memberships[row.ItemID] = row
		}
	}
	var states []EmbyItemState
	if err := tx.Where("server_id = ? AND item_id IN ?", token.ServerID, ids).Find(&states).Error; err != nil {
		return err
	}
	evidenceIDs := []uint{}
	for _, state := range states {
		cache.states[state.ItemID] = state
		if state.SnapshotID != 0 {
			evidenceIDs = append(evidenceIDs, state.SnapshotID)
		}
	}
	if len(evidenceIDs) > 0 {
		var evidence []EmbyItemEvidence
		if err := tx.Where("id IN ?", evidenceIDs).Find(&evidence).Error; err != nil {
			return err
		}
		for _, row := range evidence {
			cache.evidence[row.ID] = row
		}
		for _, id := range evidenceIDs {
			if _, ok := cache.evidence[id]; !ok {
				return gorm.ErrRecordNotFound
			}
		}
	}
	var links []EmbyMediaSyncFile
	if err := tx.Where("emby_item_id IN ?", intIDs).Order("id").Find(&links).Error; err != nil {
		return err
	}
	for _, link := range links {
		cache.links[link.EmbyItemId] = append(cache.links[link.EmbyItemId], link)
	}
	return nil
}

func loadEmbyApplySources(tx *gorm.DB, snapshots []EmbyItemSnapshot, cache *embyApplyCache) error {
	codes, roots, accounts := []string{}, []uint{}, []uint{}
	evidenceRoots := []uint{}
	for _, snapshot := range snapshots {
		for _, source := range snapshot.Sources {
			if source.PickCode != "" {
				codes = append(codes, source.PickCode)
			}
		}
		for _, scope := range snapshot.DirectoryScopes {
			evidenceRoots = append(evidenceRoots, scope.Root.SyncPathID)
			roots = append(roots, scope.Root.SyncPathID)
		}
	}
	slices.Sort(codes)
	codes = slices.Compact(codes)
	if len(codes) > 0 {
		query := tx.Clauses(clause.Locking{Strength: "SHARE"}).Where("pick_code IN ?", codes).Order("id")
		files, err := embyApplyFileCandidates(tx, "group-picks:"+embyJSON(codes), query)
		if err != nil {
			return err
		}
		cache.validations = append(cache.validations, func() error {
			// SQLite writer 保护覆盖整个事务；范围不会被并发 writer 改写。
			if tx.Dialector.Name() == "sqlite" {
				return nil
			}
			repeated := false
			for _, code := range codes {
				if cache.pickUses[code] > 1 {
					repeated = true
				}
			}
			if !repeated {
				return nil
			}
			var current []SyncFile
			if err := query.Find(&current).Error; err != nil {
				return err
			}
			if len(current) != len(files) || len(current) > 0 && !reflect.DeepEqual(current, files) {
				return ErrEmbySnapshotStale
			}
			return nil
		})
		for _, code := range codes {
			cache.picks[code] = nil
		}
		for _, file := range files {
			cache.files[file.ID] = file
			cache.picks[file.PickCode] = append(cache.picks[file.PickCode], file)
			roots = append(roots, file.SyncPathId)
			accounts = append(accounts, file.AccountId)
		}
	}
	slices.Sort(roots)
	roots = slices.Compact(roots)
	if len(roots) > 0 {
		var rows []SyncPath
		if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).Where("id IN ?", roots).Order("id").Find(&rows).Error; err != nil {
			return err
		}
		for _, id := range roots {
			cache.rootLoaded[id] = true
		}
		for _, row := range rows {
			cache.roots[row.ID] = row
			accounts = append(accounts, row.AccountId)
		}
	}
	slices.Sort(accounts)
	accounts = slices.Compact(accounts)
	if len(accounts) > 0 {
		var rows []Account
		if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).Where("id IN ?", accounts).Order("id").Find(&rows).Error; err != nil {
			return err
		}
		for _, id := range accounts {
			cache.accountLoaded[id] = true
		}
		for _, row := range rows {
			cache.accounts[row.ID] = row
		}
	}
	slices.Sort(evidenceRoots)
	evidenceRoots = slices.Compact(evidenceRoots)
	_, err := LoadSyncRootEvidenceBatchTx(tx, evidenceRoots)
	return err
}

func loadEmbyApplyRoot(tx *gorm.DB, id uint) (SyncPath, error) {
	cache := embyApplyCacheFrom(tx)
	if cache != nil && cache.rootLoaded[id] {
		row, ok := cache.roots[id]
		if !ok {
			return row, gorm.ErrRecordNotFound
		}
		return row, nil
	}
	var row SyncPath
	query := tx
	if cache != nil {
		query = query.Clauses(clause.Locking{Strength: "SHARE"})
	}
	err := query.First(&row, id).Error
	if cache != nil && (err == nil || errors.Is(err, gorm.ErrRecordNotFound)) {
		cache.rootLoaded[id] = true
		if err == nil {
			cache.roots[id] = row
		}
	}
	return row, err
}

func loadEmbyApplyAccount(tx *gorm.DB, id uint) (Account, error) {
	cache := embyApplyCacheFrom(tx)
	if cache != nil && cache.accountLoaded[id] {
		row, ok := cache.accounts[id]
		if !ok {
			return row, gorm.ErrRecordNotFound
		}
		return row, nil
	}
	var row Account
	query := tx
	if cache != nil {
		query = query.Clauses(clause.Locking{Strength: "SHARE"})
	}
	err := query.First(&row, id).Error
	if cache != nil && (err == nil || errors.Is(err, gorm.ErrRecordNotFound)) {
		cache.accountLoaded[id] = true
		if err == nil {
			cache.accounts[id] = row
		}
	}
	return row, err
}

func embyApplyFileCandidates(tx *gorm.DB, key string, query *gorm.DB) ([]SyncFile, error) {
	cache := embyApplyCacheFrom(tx)
	if cache != nil {
		if rows, ok := cache.candidates[key]; ok {
			cache.candidateUses[key]++
			return append([]SyncFile(nil), rows...), nil
		}
		query = query.Clauses(clause.Locking{Strength: "SHARE"})
	}
	var rows []SyncFile
	if err := query.Find(&rows).Error; err != nil {
		return nil, err
	}
	if cache != nil {
		cache.candidates[key] = append([]SyncFile(nil), rows...)
		cache.candidateUses[key] = 1
		for _, row := range rows {
			cache.files[row.ID] = row
		}
		expected := append([]SyncFile(nil), rows...)
		cache.validations = append(cache.validations, func() error {
			// SQLite writer 保护覆盖整个事务；范围不会被并发 writer 改写。
			if tx.Dialector.Name() == "sqlite" {
				return nil
			}
			if cache.candidateUses[key] < 2 {
				return nil
			}
			var current []SyncFile
			if err := query.Find(&current).Error; err != nil {
				return err
			}
			if len(current) != len(expected) || len(current) > 0 && !reflect.DeepEqual(current, expected) {
				return ErrEmbySnapshotStale
			}
			return nil
		})
	}
	return rows, nil
}

func applyEmbyProjection(tx *gorm.DB, item *EmbyMediaItem, existing EmbyMediaItem) error {
	if existing.ID == 0 {
		return tx.Create(item).Error
	}
	item.ID, item.CreatedAt = existing.ID, existing.CreatedAt
	updateSyncRun := item.LastSeenSyncRun != ""
	if !updateSyncRun {
		item.LastSeenSyncRun = existing.LastSeenSyncRun
	}
	before, after := existing, *item
	before.BaseModel, after.BaseModel = BaseModel{}, BaseModel{}
	if reflect.DeepEqual(before, after) {
		item.UpdatedAt = existing.UpdatedAt
		return nil
	}
	fields := make([]string, 0, 4)
	if existing.ItemIdInt != item.ItemIdInt {
		fields = append(fields, "item_id_int")
	}
	if existing.ServerId != item.ServerId {
		fields = append(fields, "server_id")
	}
	if existing.Name != item.Name {
		fields = append(fields, "name")
	}
	if existing.Type != item.Type {
		fields = append(fields, "type")
	}
	if existing.ParentId != item.ParentId {
		fields = append(fields, "parent_id")
	}
	if existing.SeriesId != item.SeriesId {
		fields = append(fields, "series_id")
	}
	if existing.SeriesName != item.SeriesName {
		fields = append(fields, "series_name")
	}
	if existing.SeasonId != item.SeasonId {
		fields = append(fields, "season_id")
	}
	if existing.SeasonName != item.SeasonName {
		fields = append(fields, "season_name")
	}
	if existing.LibraryId != item.LibraryId {
		fields = append(fields, "library_id")
	}
	if existing.Path != item.Path {
		fields = append(fields, "path")
	}
	if existing.PickCode != item.PickCode {
		fields = append(fields, "pick_code")
	}
	if existing.MediaSourcePath != item.MediaSourcePath {
		fields = append(fields, "media_source_path")
	}
	if existing.IndexNumber != item.IndexNumber {
		fields = append(fields, "index_number")
	}
	if existing.ParentIndexNumber != item.ParentIndexNumber {
		fields = append(fields, "parent_index_number")
	}
	if existing.ProductionYear != item.ProductionYear {
		fields = append(fields, "production_year")
	}
	if existing.PremiereDate != item.PremiereDate {
		fields = append(fields, "premiere_date")
	}
	if existing.DateCreated != item.DateCreated {
		fields = append(fields, "date_created")
	}
	if existing.DateCreatedTime != item.DateCreatedTime {
		fields = append(fields, "date_created_time")
	}
	if existing.DateModified != item.DateModified {
		fields = append(fields, "date_modified")
	}
	if existing.DateModifiedTime != item.DateModifiedTime {
		fields = append(fields, "date_modified_time")
	}
	if existing.IsFolder != item.IsFolder {
		fields = append(fields, "is_folder")
	}
	if existing.LastSeenAt != item.LastSeenAt {
		fields = append(fields, "last_seen_at")
	}
	if existing.PartCount != item.PartCount {
		fields = append(fields, "part_count")
	}
	if existing.PartOfItemID != item.PartOfItemID {
		fields = append(fields, "part_of_item_id")
	}
	if existing.VersionOfItemID != item.VersionOfItemID {
		fields = append(fields, "version_of_item_id")
	}
	if existing.SnapshotID != item.SnapshotID {
		fields = append(fields, "snapshot_id")
	}
	if existing.Generation != item.Generation {
		fields = append(fields, "generation")
	}
	if updateSyncRun && existing.LastSeenSyncRun != item.LastSeenSyncRun {
		fields = append(fields, "last_seen_sync_run")
	}
	return tx.Model(&existing).Select(fields).Updates(item).Error
}

func replaceEmbyLinks(tx *gorm.DB, item *EmbyMediaItem, files []EmbyFrozenFile, cache *embyApplyCache) error {
	desired := make([]EmbyMediaSyncFile, 0, len(files))
	for _, file := range files {
		current, ok := cache.files[file.SyncFileID]
		if !ok {
			if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).First(&current, file.SyncFileID).Error; err != nil {
				return err
			}
			cache.files[current.ID] = current
		}
		desired = append(desired, EmbyMediaSyncFile{EmbyItemId: uint(item.ItemIdInt), SyncFileId: file.SyncFileID, SyncPathId: file.SyncPathID, PickCode: current.PickCode, SnapshotID: item.SnapshotID, SourceID: file.SourceID})
	}
	current := cache.links[uint(item.ItemIdInt)]
	sortLinks := func(rows []EmbyMediaSyncFile) {
		slices.SortFunc(rows, func(a, b EmbyMediaSyncFile) int {
			if a.SyncFileId < b.SyncFileId {
				return -1
			}
			if a.SyncFileId > b.SyncFileId {
				return 1
			}
			if a.SourceID < b.SourceID {
				return -1
			}
			if a.SourceID > b.SourceID {
				return 1
			}
			return 0
		})
	}
	old := append([]EmbyMediaSyncFile(nil), current...)
	for i := range old {
		old[i].BaseModel = BaseModel{}
	}
	sortLinks(old)
	sortLinks(desired)
	if len(old) == len(desired) && slices.Equal(old, desired) {
		return nil
	}
	if len(current) > 0 {
		if err := tx.Where("emby_item_id = ?", item.ItemIdInt).Delete(&EmbyMediaSyncFile{}).Error; err != nil {
			return err
		}
	}
	if len(desired) > 0 {
		if err := tx.Create(&desired).Error; err != nil {
			return err
		}
	}
	cache.links[uint(item.ItemIdInt)] = desired
	return nil
}

func saveEmbyApplyMembership(tx *gorm.DB, state EmbyItemState, evidence EmbyItemEvidence, item *EmbyMediaItem) error {
	row, err := makeEmbyItemMembershipFromItem(state, evidence, item)
	if err != nil {
		return err
	}
	cache := embyApplyCacheFrom(tx)
	if cache != nil && cache.memberships != nil {
		if current, ok := cache.memberships[row.ItemID]; ok && !current.HasNull {
			previous, next := current.EmbyItemMembership, row
			previous.BaseModel, next.BaseModel = BaseModel{}, BaseModel{}
			if previous == next {
				return nil
			}
		}
	}
	columns := []string{"server_config_key", "snapshot_id", "item_type", "part_of_item_id", "season_id", "series_id"}
	conditions := make([]clause.Expression, 0, len(columns))
	for _, name := range columns {
		conditions = append(conditions, clause.Expr{SQL: "? IS DISTINCT FROM ?", Vars: []any{clause.Column{Table: "emby_item_memberships", Name: name}, clause.Column{Table: "excluded", Name: name}}})
	}
	err = tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "server_id"}, {Name: "item_id"}}, DoUpdates: clause.AssignmentColumns(append(columns, "updated_at")), Where: clause.Where{Exprs: []clause.Expression{clause.Or(conditions...)}}}).Create(&row).Error
	if err == nil && cache != nil && cache.memberships != nil {
		cache.memberships[row.ItemID] = embyApplyMembershipRow{EmbyItemMembership: row}
	}
	return err
}

func saveEmbyApplyLibraryRelations(tx *gorm.DB, snapshots []EmbyItemSnapshot, cache *embyApplyCache) error {
	relations := map[string]EmbyLibrarySyncPath{}
	for _, snapshot := range snapshots {
		if snapshot.Item.LibraryId != "" {
			for _, file := range cache.links[uint(snapshot.Item.ItemIdInt)] {
				row := EmbyLibrarySyncPath{LibraryId: snapshot.Item.LibraryId, SyncPathId: file.SyncPathId, LibraryName: snapshot.LibraryName}
				relations[embyJSON([]any{row.LibraryId, row.SyncPathId})] = row
			}
		}
	}
	// SQLite writer 或 PG 行共享锁保证已存在关联在本事务内不被删除。
	if len(relations) > 0 {
		predicates := make([]string, 0, len(relations))
		args := make([]any, 0, 2*len(relations))
		for _, row := range relations {
			predicates = append(predicates, "(library_id = ? AND sync_path_id = ?)")
			args = append(args, row.LibraryId, row.SyncPathId)
		}
		var existing []EmbyLibrarySyncPath
		if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).Select("library_id", "sync_path_id").Where(strings.Join(predicates, " OR "), args...).Find(&existing).Error; err != nil {
			return err
		}
		for _, row := range existing {
			delete(relations, embyJSON([]any{row.LibraryId, row.SyncPathId}))
		}
	}
	rows := make([]EmbyLibrarySyncPath, 0, len(relations))
	for _, row := range relations {
		rows = append(rows, row)
	}
	slices.SortFunc(rows, func(a, b EmbyLibrarySyncPath) int {
		if a.LibraryId < b.LibraryId {
			return -1
		}
		if a.LibraryId > b.LibraryId {
			return 1
		}
		if a.SyncPathId < b.SyncPathId {
			return -1
		}
		if a.SyncPathId > b.SyncPathId {
			return 1
		}
		return 0
	})
	if len(rows) == 0 {
		return nil
	}
	// 单次批量冲突插入，所有组使用同一顺序，避免多根相反顺序的唯一键等待环。
	return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&rows).Error
}

func loadEmbyApplySourceAccounts(tx *gorm.DB, sourceType SourceType, userID string) ([]Account, error) {
	cache := embyApplyCacheFrom(tx)
	key := embyJSON([]any{sourceType, userID})
	if cache != nil {
		if rows, ok := cache.sourceAccounts[key]; ok {
			cache.sourceAccountUses[key]++
			return rows, nil
		}
		tx = tx.Clauses(clause.Locking{Strength: "SHARE"}).Session(&gorm.Session{})
	}
	var rows []Account
	if err := tx.Where("source_type = ? AND user_id = ?", sourceType, userID).Order("id").Limit(2).Find(&rows).Error; err != nil {
		return nil, err
	}
	if cache != nil {
		cache.sourceAccounts[key] = rows
		cache.sourceAccountUses[key] = 1
		for _, row := range rows {
			cache.accountLoaded[row.ID], cache.accounts[row.ID] = true, row
		}
		expected := append([]Account(nil), rows...)
		cache.validations = append(cache.validations, func() error {
			// SQLite writer 保护覆盖整个事务；范围不会被并发 writer 改写。
			if tx.Dialector.Name() == "sqlite" {
				return nil
			}
			if cache.sourceAccountUses[key] < 2 {
				return nil
			}
			var current []Account
			if err := tx.Where("source_type = ? AND user_id = ?", sourceType, userID).Order("id").Limit(2).Find(&current).Error; err != nil {
				return err
			}
			if len(current) != len(expected) || len(current) > 0 && !reflect.DeepEqual(current, expected) {
				return ErrEmbySnapshotStale
			}
			return nil
		})
	}
	return rows, nil
}
