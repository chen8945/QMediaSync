package embyclientrestgo

import (
	"context"
	"net/url"
)

// GetItemAncestorsContext 在同步观察期间读取真实媒体祖先，支持生命周期取消。
func (c *Client) GetItemAncestorsContext(ctx context.Context, itemID string) ([]AncestorDto, error) {
	var ancestors []AncestorDto
	if err := c.getSnapshotJSON(ctx, "/Items/"+url.PathEscape(itemID)+"/Ancestors", nil, &ancestors); err != nil {
		return nil, err
	}
	return ancestors, nil
}
