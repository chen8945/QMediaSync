package models

import (
	"fmt"
	"path/filepath"
	"strings"
	"unicode"
)

// EmbyPathMapping 将 Emby 看到的 STRM 根映射到 QMS 可访问的挂载根。
// SyncPathID 仅表示用户明确声明相对目录结构与该同步根一致，不由挂载路径推导。
type EmbyPathMapping struct {
	EmbyRoot   string `json:"emby_root"`
	LocalRoot  string `json:"local_root"`
	SyncPathID uint   `json:"sync_path_id,omitempty"`
}

// NormalizeEmbyPathMappings 校验绝对根、路径组件和重叠映射，返回独立的规范化副本。
func NormalizeEmbyPathMappings(mappings []EmbyPathMapping) ([]EmbyPathMapping, error) {
	normalized := make([]EmbyPathMapping, 0, len(mappings))
	for i, mapping := range mappings {
		embyRoot, _, err := normalizeEmbyMappingPath(mapping.EmbyRoot)
		if err != nil {
			return nil, fmt.Errorf("第 %d 行 Emby 根目录：%w", i+1, err)
		}
		if !filepath.IsAbs(mapping.LocalRoot) || invalidMappingComponents(filepath.ToSlash(mapping.LocalRoot)) {
			return nil, fmt.Errorf("第 %d 行 QMS 可访问根目录必须是绝对路径，且不能包含 .、.. 或控制字符", i+1)
		}
		mapping.EmbyRoot = embyRoot
		mapping.LocalRoot = filepath.Clean(mapping.LocalRoot)
		for _, previous := range normalized {
			_, insidePrevious := previous.RelativePath(mapping.EmbyRoot)
			_, containsPrevious := mapping.RelativePath(previous.EmbyRoot)
			if insidePrevious || containsPrevious {
				return nil, fmt.Errorf("第 %d 行 Emby 根目录与已有映射重复或重叠", i+1)
			}
		}
		normalized = append(normalized, mapping)
	}
	return normalized, nil
}

// RelativePath 按完整路径组件匹配 Emby 根，返回使用 / 分隔的相对路径。
// Emby 可以运行于 Windows；其盘符和 UNC 路径在 Linux 上也按 Windows 语义匹配。
func (mapping EmbyPathMapping) RelativePath(embyPath string) (string, bool) {
	root, windowsRoot, err := normalizeEmbyMappingPath(mapping.EmbyRoot)
	if err != nil {
		return "", false
	}
	candidate, windowsPath, err := normalizeEmbyMappingPath(embyPath)
	if err != nil || windowsRoot != windowsPath {
		return "", false
	}
	rootParts := strings.Split(strings.TrimRight(root, "/"), "/")
	candidateParts := strings.Split(strings.TrimRight(candidate, "/"), "/")
	if len(candidateParts) < len(rootParts) {
		return "", false
	}
	for i, part := range rootParts {
		if part != candidateParts[i] && (!windowsRoot || !strings.EqualFold(part, candidateParts[i])) {
			return "", false
		}
	}
	if len(rootParts) == len(candidateParts) {
		return ".", true
	}
	return strings.Join(candidateParts[len(rootParts):], "/"), true
}

// ResolveLocalPath 只进行挂载路径转换，不赋予远端目录删除权限。
func (mapping EmbyPathMapping) ResolveLocalPath(embyPath string) (string, bool) {
	relative, ok := mapping.RelativePath(embyPath)
	if !ok || !filepath.IsAbs(mapping.LocalRoot) || invalidMappingComponents(filepath.ToSlash(mapping.LocalRoot)) {
		return "", false
	}
	localRelative := filepath.FromSlash(relative)
	if !filepath.IsLocal(localRelative) {
		return "", false
	}
	return filepath.Join(mapping.LocalRoot, localRelative), true
}

// inferEmbyPathMapping 只为已确认的同一份 STRM 推导前缀；已知本地根决定完整相对路径边界。
func inferEmbyPathMapping(embyPath, localRoot, localPath string) (EmbyPathMapping, bool) {
	candidate, windows, err := normalizeEmbyMappingPath(embyPath)
	if err != nil || !filepath.IsAbs(localRoot) || !filepath.IsAbs(localPath) ||
		filepath.Clean(localRoot) != localRoot || filepath.Clean(localPath) != localPath ||
		invalidMappingComponents(filepath.ToSlash(localRoot)) || invalidMappingComponents(filepath.ToSlash(localPath)) {
		return EmbyPathMapping{}, false
	}
	relative, err := filepath.Rel(localRoot, localPath)
	if err != nil || relative == "." || !filepath.IsLocal(relative) {
		return EmbyPathMapping{}, false
	}
	localParts := strings.Split(filepath.ToSlash(relative), "/")
	embyParts := strings.Split(candidate, "/")
	prefixLength := len(embyParts) - len(localParts)
	if prefixLength < 1 {
		return EmbyPathMapping{}, false
	}
	for i, part := range localParts {
		other := embyParts[prefixLength+i]
		if part != other && (!windows || !strings.EqualFold(part, other)) {
			return EmbyPathMapping{}, false
		}
	}
	embyRoot := strings.Join(embyParts[:prefixLength], "/")
	if embyRoot == "" || windows && strings.HasSuffix(embyRoot, ":") {
		embyRoot += "/"
	}
	embyRoot, _, err = normalizeEmbyMappingPath(embyRoot)
	if err != nil {
		return EmbyPathMapping{}, false
	}
	return EmbyPathMapping{EmbyRoot: embyRoot, LocalRoot: localRoot}, true
}

func normalizeEmbyMappingPath(value string) (string, bool, error) {
	windows := strings.HasPrefix(value, `\\`) || strings.HasPrefix(value, "//") ||
		(len(value) >= 3 && isEmbyDriveLetter(value[0]) && value[1] == ':' && (value[2] == '/' || value[2] == '\\'))
	if windows {
		value = strings.ReplaceAll(value, `\`, "/")
		components := value
		if !strings.HasPrefix(value, "//") {
			components = value[3:]
		}
		for _, component := range strings.Split(components, "/") {
			if strings.ContainsAny(component, `<>:"|?*`) || strings.TrimRight(component, " .") != component {
				return "", true, fmt.Errorf("Windows 路径不能包含保留字符或以空格、点结尾的组件")
			}
		}
	} else if strings.Contains(value, `\`) {
		return "", false, fmt.Errorf("POSIX 路径不能包含反斜杠")
	}
	if value == "" || invalidMappingComponents(value) {
		return "", windows, fmt.Errorf("必须是绝对路径，且不能包含 .、.. 或控制字符")
	}
	if windows && strings.HasPrefix(value, "//") {
		parts := strings.FieldsFunc(value, func(r rune) bool { return r == '/' })
		if len(parts) < 2 {
			return "", windows, fmt.Errorf("UNC 路径必须包含服务器和共享目录")
		}
		return "//" + strings.Join(parts, "/"), true, nil
	}
	if windows {
		parts := strings.FieldsFunc(value[3:], func(r rune) bool { return r == '/' })
		return strings.ToUpper(value[:1]) + ":/" + strings.Join(parts, "/"), true, nil
	}
	if !strings.HasPrefix(value, "/") {
		return "", false, fmt.Errorf("必须是绝对路径")
	}
	parts := strings.FieldsFunc(value, func(r rune) bool { return r == '/' })
	return "/" + strings.Join(parts, "/"), false, nil
}

func invalidMappingComponents(value string) bool {
	if strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return true
	}
	for _, component := range strings.Split(value, "/") {
		if component == "." || component == ".." {
			return true
		}
	}
	return false
}

func isEmbyDriveLetter(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z'
}
