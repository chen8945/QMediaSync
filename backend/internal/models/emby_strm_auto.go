package models

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"gorm.io/gorm"

	"qmediasync/internal/db"
)

const (
	embyAutoSTRMMaxCandidates = 256
	embyAutoSTRMMaxDepth      = 64
	embyAutoSTRMMaxBytes      = 64 << 10
)

type embySTRMObservation struct {
	digest   string
	fileInfo os.FileInfo
	rootInfo os.FileInfo
}

// embyPreparedSTRMReference 只在本次准备和提交之间使用，不持久化原始 STRM 内容。
type embyPreparedSTRMReference struct {
	source      EmbySnapshotSource
	reference   EmbySTRMReference
	observation embySTRMObservation
}

func (c *EmbyDirectoryEvidenceCollector) prepareSTRMReferences(ctx context.Context, snapshot *EmbyItemSnapshot, files []EmbyFrozenFile) error {
	snapshot.preparedSTRMReferences = nil
	if len(files) == len(snapshot.Sources) {
		return nil
	}
	sources, err := freezeEmbySnapshotSources(db.Db.WithContext(ctx), *snapshot, files)
	if err != nil {
		return err
	}
	for i := range sources {
		source := &sources[i]
		if source.SyncFileID != 0 || source.Reference.LocalRoot != "" || source.Reference.Reason != "" ||
			source.AccountID == 0 || source.AccountIdentity == "" || source.ObjectID == "" {
			continue
		}
		prepared, err := c.locateSTRMReference(ctx, snapshot.Item.Path, *source)
		if err != nil {
			return err
		}
		if snapshot.preparedSTRMReferences == nil {
			snapshot.preparedSTRMReferences = map[int]embyPreparedSTRMReference{}
		}
		snapshot.preparedSTRMReferences[i] = prepared
		source.Reference = prepared.reference
	}
	snapshot.frozenSources = sources
	return nil
}

func (c *EmbyDirectoryEvidenceCollector) locateSTRMReference(ctx context.Context, embyPath string, source EmbySnapshotSource) (embyPreparedSTRMReference, error) {
	source.Reference = EmbySTRMReference{}
	prepared := embyPreparedSTRMReference{source: source, reference: EmbySTRMReference{EmbyPath: sanitizeEmbyEvidencePath(embyPath)}}
	normalized, windows, err := normalizeEmbyMappingPath(embyPath)
	if err != nil || !strings.EqualFold(filepath.Ext(normalized), ".strm") {
		return prepared, nil
	}
	parts := strings.Split(normalized, "/")
	first := 1
	if windows && strings.HasPrefix(normalized, "//") {
		first = 4 // UNC 的服务器和共享目录属于前缀，不能成为根下候选。
	}
	if len(parts) <= first || len(parts)-first > embyAutoSTRMMaxDepth {
		return prepared, nil
	}
	key := embyDigest([]any{source.SourceType, source.AccountID, source.AccountIdentity})
	if c.strmRoots == nil {
		c.strmRoots = map[string][]SyncPath{}
	}
	roots, found := c.strmRoots[key]
	if !found {
		if err := db.Db.WithContext(ctx).Where("source_type = ? AND account_id = ?", source.SourceType, source.AccountID).
			Order("id").Limit(embyAutoSTRMMaxCandidates + 1).Find(&roots).Error; err != nil {
			return prepared, err
		}
		c.strmRoots[key] = roots
	}
	// shortcut: 仅探测已知根与路径后缀的有限组合；更复杂的外部目录需先明确根，不能退化为全盘扫描。
	if len(roots) > embyAutoSTRMMaxCandidates/(len(parts)-first) {
		prepared.reference.Reason = "strm_candidate_limit"
		return prepared, nil
	}
	unreadable := false
	for _, root := range roots {
		for start := first; start < len(parts); start++ {
			if err := ctx.Err(); err != nil {
				return prepared, err
			}
			local := filepath.Join(root.GetFullLocalPath(), filepath.FromSlash(strings.Join(parts[start:], "/")))
			reference := embyAutoSTRMReference(embyPath, local, root, source.AccountIdentity)
			if reference.AutoSyncPathID == 0 {
				continue
			}
			content, observation, err := readEmbyAutoSTRM(reference)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				unreadable = true
				continue
			}
			if !embySTRMContentMatchesSource(content, source) {
				continue
			}
			if prepared.reference.LocalPath != "" && prepared.reference.LocalPath != local {
				prepared.reference = EmbySTRMReference{EmbyPath: prepared.reference.EmbyPath, Reason: "ambiguous_strm_path"}
				return prepared, nil
			}
			if prepared.reference.LocalRoot == "" || len(reference.LocalRoot) > len(prepared.reference.LocalRoot) {
				prepared.reference, prepared.observation = reference, observation
			}
		}
	}
	if unreadable {
		prepared.reference = EmbySTRMReference{EmbyPath: prepared.reference.EmbyPath, Reason: "strm_candidate_unreadable"}
	}
	return prepared, nil
}

// embySTRMContentMatchesSource 复用原来源已确认的账号及路由，不以文件名、路径尾段或任意 URL 的同码建立身份。
func embySTRMContentMatchesSource(content string, source EmbySnapshotSource) bool {
	content = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(content), "\ufeff"))
	original, _, ok := embySourceURL(source.Path)
	if !ok {
		return false
	}
	candidate, _, ok := embySourceURL(content)
	if !ok || original.Scheme != candidate.Scheme || !strings.EqualFold(original.Host, candidate.Host) {
		return false
	}
	if source.SourceType == SourceTypeOpenList {
		return source.ObjectKind == "openlist_path" && original.Path == candidate.Path
	}
	origin := original.Scheme + "://" + original.Host
	bases := []string{origin}
	for _, marker := range []string{"/115/", "/baidupan/"} {
		if index := strings.LastIndex(original.Path, marker); index > 0 {
			bases = append(bases, origin+original.Path[:index])
		}
	}
	wantType, wantUser, wantObject, known := EmbySourceLookupIdentity(source, bases)
	gotType, gotUser, gotObject, matched := EmbySourceLookupIdentity(EmbySnapshotSource{Path: content}, bases)
	return known && matched && wantType == source.SourceType && wantObject == source.ObjectID &&
		wantType == gotType && wantUser == gotUser && wantObject == gotObject
}

func readEmbyAutoSTRM(reference EmbySTRMReference) (string, embySTRMObservation, error) {
	invalid := func() (string, embySTRMObservation, error) { return "", embySTRMObservation{}, ErrEmbyDeleteUnverified }
	if _, ok := inferEmbyPathMapping(reference.EmbyPath, reference.LocalRoot, reference.LocalPath); !ok ||
		!strings.EqualFold(filepath.Ext(reference.LocalPath), ".strm") {
		return invalid()
	}
	rootBefore, err := os.Lstat(reference.LocalRoot)
	if err != nil {
		return invalid()
	}
	if !rootBefore.IsDir() {
		return invalid()
	}
	root, err := os.OpenRoot(reference.LocalRoot)
	if err != nil {
		return invalid()
	}
	defer root.Close()
	rootInfo, err := root.Stat(".")
	if err != nil || !os.SameFile(rootBefore, rootInfo) {
		return invalid()
	}
	relative, _ := filepath.Rel(reference.LocalRoot, reference.LocalPath)
	current := "."
	var before os.FileInfo
	for _, part := range strings.Split(relative, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		before, err = root.Lstat(current)
		if err != nil {
			return "", embySTRMObservation{}, err
		}
		if before.Mode()&os.ModeSymlink != 0 {
			return invalid()
		}
	}
	if before == nil || !before.Mode().IsRegular() || before.Size() > embyAutoSTRMMaxBytes {
		return invalid()
	}
	file, err := root.Open(relative)
	if err != nil {
		return "", embySTRMObservation{}, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !sameEmbySTRMFile(before, opened) {
		return invalid()
	}
	content, err := io.ReadAll(io.LimitReader(file, embyAutoSTRMMaxBytes+1))
	if err != nil {
		return "", embySTRMObservation{}, err
	}
	after, err := root.Lstat(relative)
	if err != nil || len(content) > embyAutoSTRMMaxBytes || !sameEmbySTRMFile(before, after) {
		return invalid()
	}
	rootAfter, err := os.Lstat(reference.LocalRoot)
	if err != nil || !os.SameFile(rootInfo, rootAfter) {
		return invalid()
	}
	return string(content), embySTRMObservation{digest: embyDigest(string(content)), fileInfo: after, rootInfo: rootInfo}, nil
}

func sameEmbySTRMFile(left, right os.FileInfo) bool {
	return left != nil && right != nil && left.Mode().IsRegular() && right.Mode().IsRegular() &&
		os.SameFile(left, right) && left.Size() == right.Size() && left.ModTime().Equal(right.ModTime())
}

func validatePreparedEmbySTRMReference(tx *gorm.DB, config *EmbyConfig, source EmbySnapshotSource, prepared embyPreparedSTRMReference) error {
	source.Reference = EmbySTRMReference{}
	if source != prepared.source {
		return ErrEmbySnapshotStale
	}
	reference := prepared.reference
	explicit := ResolveEmbySTRMReference(config, reference.EmbyPath)
	if explicit.LocalRoot != "" || explicit.Reason != "" {
		return ErrEmbySnapshotStale
	}
	if reference.AutoSyncPathID == 0 {
		return nil
	}
	root, rootErr := loadEmbyApplyRoot(tx, reference.AutoSyncPathID)
	if err := rootErr; err != nil {
		return err
	}
	if root.SourceType != source.SourceType || root.AccountId != source.AccountID {
		return ErrEmbySnapshotStale
	}
	if err := validateEmbyAutoSTRMReference(config, reference, root, source.AccountIdentity); err != nil {
		return err
	}
	_, observation, err := readEmbyAutoSTRM(reference)
	if err != nil || observation.digest != prepared.observation.digest ||
		!sameEmbySTRMFile(observation.fileInfo, prepared.observation.fileInfo) ||
		!os.SameFile(observation.rootInfo, prepared.observation.rootInfo) {
		return ErrEmbySnapshotStale
	}
	return nil
}
