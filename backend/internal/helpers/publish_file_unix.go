//go:build !windows

package helpers

import (
	"errors"
	"os"
	"path/filepath"
)

func publishSyncedFileNoReplace(source, target string) error {
	if err := publishMissingMetadata(source, target, os.Link, renameMetadataNoReplace); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(target))
	if err != nil {
		return err
	}
	err = dir.Sync()
	return errors.Join(err, dir.Close())
}
