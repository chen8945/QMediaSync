package models

import "context"

type embyDirectoryProviderKey struct{}

// WithEmbyDirectoryProvider 让目录存活核验复用本次计划或执行已选定的 provider。
func WithEmbyDirectoryProvider(ctx context.Context, provider EmbyDeleteProvider) context.Context {
	if provider == nil {
		return ctx
	}
	return context.WithValue(ctx, embyDirectoryProviderKey{}, provider)
}

// ListEmbyDeletionDirectory 只读取待删目录的直接子项，共用文件模式的完整清单缓存。
// 最终发送检查只能使用未过期的清单，不能在供应商队列内排入新的读取。
func ListEmbyDeletionDirectory(ctx context.Context, scope EmbyDirectoryScope) ([]EmbyRemoteFile, error) {
	provider, _ := ctx.Value(embyDirectoryProviderKey{}).(EmbyDeleteProvider)
	if provider == nil {
		return nil, ErrEmbyDeleteUnverified
	}
	file := scope.Root
	file.Path, file.ParentID = embyDirectoryFullPath(scope), scope.Root.FileID
	if file.SourceType == SourceTypeBaiduPan {
		// 百度文件账本以父路径分组，目录自身才使用 fsid；与文件模式共用同一个键。
		file.ParentID = embyDeleteDirectory(file.Path)
	}
	if concrete, ok := provider.(*embyDeleteProvider); ok {
		return concrete.List(ctx, file)
	}
	return embyCachedDeletionListing(ctx, file, func() ([]EmbyRemoteFile, error) { return provider.List(ctx, file) })
}
