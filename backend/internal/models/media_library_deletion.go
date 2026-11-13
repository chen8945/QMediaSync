package models

// BindMediaServerType 恢复已验证父记录的归属，不修改冻结 JSON 或历史摘要。
func (input *EmbyDeletionInput) BindMediaServerType(kind MediaServerType) error {
	if err := kind.Validate(); err != nil {
		return err
	}
	input.MediaServerType = kind
	for i := range input.Owners {
		owner := &input.Owners[i]
		owner.Item.MediaServerType = kind
		owner.Evidence.MediaServerType = kind
		for j := range owner.Links {
			owner.Links[j].MediaServerType = kind
		}
	}
	return nil
}
func sameEmbyFrozenEvidence(current, frozen EmbyItemEvidence, kind MediaServerType) bool {
	if kind.Validate() != nil || current.MediaServerType != kind {
		return false
	}
	frozen.MediaServerType = kind
	return current == frozen
}
