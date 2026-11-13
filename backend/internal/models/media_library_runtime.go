package models

import embyclientrestgo "qmediasync/internal/medialibrary/client"

// Client 绑定配置所属端的预期产品，远端明确错配由共用客户端拒绝。
func (c *EmbyConfig) Client() *embyclientrestgo.Client {
	expected := embyclientrestgo.Product("invalid")
	if c.MediaServerType == MediaServerEmby || c.MediaServerType == MediaServerJellyfin {
		expected = embyclientrestgo.Product(c.MediaServerType)
	}
	return embyclientrestgo.NewClientForProduct(c.EmbyUrl, c.EmbyApiKey, expected)
}
