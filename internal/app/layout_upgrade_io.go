package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"duplex/internal/catalog"
	"duplex/internal/config"
	"duplex/internal/media"
)

func upgradeOneLayout(ctx context.Context, dir string, store *catalog.Store, id string, apply bool) (bool, error) {
	video, err := store.Video(ctx, id)
	if err != nil {
		return false, err
	}

	path := filepath.Join(dir, video.Revision+".mp4-index.json")

	old, err := media.LoadLayout(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}

	if err != nil {
		return false, err
	}

	if old.ID() != video.Revision || old.Size() != video.Size {
		return false, media.ErrChanged
	}

	if old.Revision == media.PackagingVersion {
		return false, nil
	}

	next, err := media.Repack(ctx, old)
	if err != nil {
		return false, err
	}

	descriptor, err := readLayoutDescription(dir, video)
	if err != nil {
		return false, err
	}

	if descriptor.Media != next.Details() {
		return false, media.ErrChanged
	}

	alias, err := layoutUpgradeAlias(dir, old, next, descriptor)
	if err != nil {
		return false, err
	}

	if !apply {
		return true, nil
	}

	if err = persistLayoutUpgrade(dir, alias, next, descriptor); err != nil {
		return false, err
	}

	return true, store.UpgradeLayoutRevision(ctx, id, old.ID(), next.ID(), next.Size())
}

func readLayoutDescription(dir string, video catalog.Video) (representationDescription, error) {
	var descriptor representationDescription

	path := filepath.Join(dir, video.Revision+".description.json")

	info, err := os.Stat(path)
	if err != nil {
		return descriptor, err
	}

	if !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return descriptor, media.ErrUnsupported
	}

	data, err := os.ReadFile(path) //nolint:gosec // G304: The retained revision selects a private descriptor beneath the configured index directory.
	if err != nil {
		return descriptor, err
	}

	if err = json.Unmarshal(data, &descriptor); err != nil {
		return descriptor, err
	}

	if descriptor.VideoID != video.ID || descriptor.Revision != video.Revision || descriptor.Size != video.Size || descriptor.VideoFormat == "" || descriptor.AudioFormat == "" {
		return descriptor, media.ErrChanged
	}

	return descriptor, nil
}

func persistLayoutUpgrade(dir, alias string, next *media.VirtualMP4, descriptor representationDescription) error {
	if err := next.SaveIndex(filepath.Join(dir, next.ID()+".mp4-index.json")); err != nil {
		return err
	}

	descriptor.Revision, descriptor.Size = next.ID(), next.Size()

	data, err := json.Marshal(descriptor)
	if err != nil {
		return err
	}

	if err = config.AtomicWriteIfChanged(filepath.Join(dir, next.ID()+".description.json"), data, 0o600); err != nil {
		return err
	}

	// Publish the alias before the catalogue pointer. Old immutable files remain
	// valid if interrupted; repeating the command completes the upgrade.
	return next.SaveIndex(alias)
}

func layoutUpgradeAlias(dir string, old, next *media.VirtualMP4, descriptor representationDescription) (string, error) {
	key := sha256.Sum256([]byte(descriptor.VideoID + ":" + descriptor.VideoFormat + ":" + descriptor.AudioFormat))
	alias := filepath.Join(dir, hex.EncodeToString(key[:])+".json")

	saved, err := media.LoadLayout(alias)
	if err != nil {
		return "", err
	}

	if saved.ID() != old.ID() && saved.ID() != next.ID() {
		return "", media.ErrChanged
	}

	return alias, nil
}
