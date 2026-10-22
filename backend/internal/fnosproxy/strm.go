package fnosproxy

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"qmediasync/internal/models"
)

const strmLimit = 64 << 10

// Source 是已用当前飞牛会话核验的准确媒体源。
type Source struct {
	ItemID string
	ID     string
	Path   string
	URL    string
}

// Candidate 是账本按可靠源身份找到的候选，不以文件名或唯一结果证明身份。
type Candidate struct {
	Root string
	Path string
	URL  string
}

func (inst *instance) roots(ctx context.Context) ([]string, error) {
	if inst.service.options.OutputRoots == nil {
		return []string{}, nil
	}
	roots, err := inst.service.options.OutputRoots(ctx)
	if err != nil {
		return nil, err
	}
	normalized := make([]string, 0, len(roots))
	for _, root := range roots {
		if filepath.IsAbs(root) && filepath.Clean(root) == root && root != string(filepath.Separator) {
			normalized = append(normalized, root)
		}
	}
	slices.Sort(normalized)
	return slices.Compact(normalized), nil
}

func (inst *instance) resolveSource(ctx context.Context, r *http.Request, source Source) (sourceRecord, bool) {
	if !strings.EqualFold(filepath.Ext(strings.ReplaceAll(source.Path, `\`, "/")), ".strm") {
		return sourceRecord{}, false
	}
	roots, err := inst.roots(ctx)
	if err != nil || ctx.Err() != nil {
		return sourceRecord{}, false
	}
	scope := fingerprint([]any{inst.config.FnosURL, inst.config.PathMappings, roots})
	generation := inst.cache.begin(scope)
	key := fingerprint([]string{scope, credentialFingerprint(r), source.ItemID, source.ID})
	if previous, ok := inst.cache.get(key, generation); ok {
		if previous.source == source {
			current, _, err := openSTRM(previous.root, previous.relative, false)
			if err == nil && sameFile(previous.fileInfo, current.fileInfo) && os.SameFile(previous.rootInfo, current.rootInfo) &&
				inst.currentRoots(ctx, roots) {
				return previous, true
			}
		}
		inst.cache.remove(key, generation)
	}
	candidates, ok := inst.locate(ctx, source, roots)
	if !ok || len(candidates) != 1 {
		return sourceRecord{}, false
	}
	candidate := candidates[0]
	relative, err := filepath.Rel(candidate.Root, candidate.Path)
	if err != nil {
		return sourceRecord{}, false
	}
	record, content, err := openSTRM(candidate.Root, relative, true)
	if err != nil {
		return sourceRecord{}, false
	}
	record.url, ok = strmURL(content)
	if !ok || candidate.URL != "" && !sameSourceURL(candidate.URL, record.url) ||
		!inst.currentRoots(ctx, roots) || inst.ctx.Err() != nil {
		return sourceRecord{}, false
	}
	record.source = source
	if inst.stableSourceURL(ctx, record.url) {
		inst.cache.put(key, record, generation)
	}
	return record, true
}

func (inst *instance) currentRoots(ctx context.Context, previous []string) bool {
	current, err := inst.roots(ctx)
	return err == nil && slices.Equal(current, previous) && ctx.Err() == nil && inst.ctx.Err() == nil
}

func (inst *instance) locate(ctx context.Context, source Source, roots []string) ([]Candidate, bool) {
	for _, mapping := range inst.config.PathMappings {
		adapted := models.EmbyPathMapping{EmbyRoot: mapping.FnosRoot, LocalRoot: mapping.LocalRoot}
		if _, matched := adapted.RelativePath(source.Path); matched {
			local, ok := adapted.ResolveLocalPath(source.Path)
			if !ok {
				return nil, false
			}
			return []Candidate{{Root: mapping.LocalRoot, Path: local}}, true
		}
	}
	if filepath.IsAbs(source.Path) && filepath.Clean(source.Path) == source.Path {
		var match Candidate
		for _, root := range roots {
			relative, err := filepath.Rel(root, source.Path)
			if err == nil && relative != "." && filepath.IsLocal(relative) && len(root) > len(match.Root) {
				match = Candidate{Root: root, Path: source.Path}
			}
		}
		if match.Root != "" {
			return []Candidate{match}, true
		}
	}
	if source.URL == "" || inst.service.options.IdentityCandidates == nil {
		return nil, false
	}
	// shortcut: 自动定位仅接收账本按可靠源身份给出的有限候选；其他异挂载需要用户声明根映射。
	candidates, err := inst.service.options.IdentityCandidates(ctx, source)
	if err != nil || len(candidates) > sourceCacheCapacity {
		return nil, false
	}
	var matches []Candidate
	for _, candidate := range candidates {
		if !slices.Contains(roots, candidate.Root) || !sameSourceURL(source.URL, candidate.URL) {
			continue
		}
		relative, err := filepath.Rel(candidate.Root, candidate.Path)
		if err != nil || !filepath.IsLocal(relative) || relative == "." {
			continue
		}
		_, content, err := openSTRM(candidate.Root, relative, true)
		if err != nil {
			continue
		}
		parsed, ok := strmURL(content)
		if ok && sameSourceURL(source.URL, parsed) {
			duplicate := slices.ContainsFunc(matches, func(previous Candidate) bool { return previous.Path == candidate.Path })
			if !duplicate {
				matches = append(matches, candidate)
			}
		}
	}
	return matches, len(matches) == 1
}

func openSTRM(rootPath, relative string, read bool) (sourceRecord, string, error) {
	invalid := func() (sourceRecord, string, error) {
		return sourceRecord{}, "", errors.New("STRM 文件不可安全读取")
	}
	if !filepath.IsAbs(rootPath) || filepath.Clean(rootPath) != rootPath || relative == "." || !filepath.IsLocal(relative) ||
		!strings.EqualFold(filepath.Ext(relative), ".strm") {
		return invalid()
	}
	rootBefore, err := os.Lstat(rootPath)
	if err != nil || !rootBefore.IsDir() || rootBefore.Mode()&os.ModeSymlink != 0 {
		return invalid()
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return invalid()
	}
	defer root.Close()
	rootInfo, err := root.Stat(".")
	if err != nil || !os.SameFile(rootBefore, rootInfo) {
		return invalid()
	}
	current := "."
	var before os.FileInfo
	for _, component := range strings.Split(relative, string(filepath.Separator)) {
		if component == "" || component == "." || component == ".." {
			return invalid()
		}
		current = filepath.Join(current, component)
		before, err = root.Lstat(current)
		if err != nil || before.Mode()&os.ModeSymlink != 0 {
			return invalid()
		}
	}
	if before == nil || !before.Mode().IsRegular() || before.Size() > strmLimit {
		return invalid()
	}
	file, err := root.Open(relative)
	if err != nil {
		return invalid()
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !sameFile(before, opened) {
		return invalid()
	}
	var content []byte
	if read {
		content, err = io.ReadAll(io.LimitReader(file, strmLimit+1))
		if err != nil || len(content) > strmLimit {
			return invalid()
		}
	}
	afterOpen, openErr := file.Stat()
	afterPath, pathErr := root.Lstat(relative)
	rootAfter, rootErr := os.Lstat(rootPath)
	if openErr != nil || pathErr != nil || rootErr != nil || !sameFile(opened, afterOpen) ||
		!sameFile(opened, afterPath) || !os.SameFile(rootInfo, rootAfter) {
		return invalid()
	}
	return sourceRecord{root: rootPath, relative: relative, fileInfo: afterOpen, rootInfo: rootInfo}, string(content), nil
}

func sameFile(a, b os.FileInfo) bool {
	return a != nil && b != nil && a.Mode().IsRegular() && b.Mode().IsRegular() && os.SameFile(a, b) &&
		a.Mode() == b.Mode() && a.Size() == b.Size() && a.ModTime().Equal(b.ModTime())
}

func strmURL(content string) (string, bool) {
	content = strings.TrimPrefix(strings.TrimSpace(content), "\ufeff")
	var result string
	for line := range strings.SplitSeq(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parsed, err := url.Parse(line)
		if err != nil || parsed.Host == "" || parsed.User != nil || parsed.Opaque != "" ||
			(parsed.Scheme != "http" && parsed.Scheme != "https") || strings.ContainsAny(line, "\r\x00") {
			return "", false
		}
		if result != "" && result != line {
			return "", false
		}
		result = line
	}
	return result, result != ""
}

func sameSourceURL(a, b string) bool {
	// 精确 URL 是可靠身份的保守子集，不重排签名或猜测不同 URL 指向同一文件。
	return a != "" && a == b
}

func (inst *instance) stableSourceURL(ctx context.Context, raw string) bool {
	if _, ok := inst.internalURL(ctx, raw); ok {
		return true
	}
	u, err := url.Parse(raw)
	if err != nil || inst.service.options.OpenListOrigins == nil {
		return false
	}
	// OpenList 的 /d/ 是稳定取链入口；其他外部地址一律下次重读，不推测 CDN 签名有效期。
	for key := range u.Query() {
		if key != "sign" {
			return false
		}
	}
	origins, err := inst.service.options.OpenListOrigins(ctx)
	if err != nil {
		return false
	}
	for _, rawOrigin := range origins {
		origin, err := url.Parse(rawOrigin)
		if err == nil && origin.Scheme == u.Scheme && strings.EqualFold(origin.Host, u.Host) &&
			strings.HasPrefix(u.Path, strings.TrimRight(origin.Path, "/")+"/d/") {
			return true
		}
	}
	return false
}
