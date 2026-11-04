package app

import (
	"context"
	"errors"
	"path/filepath"

	"duplex/internal/cache"
	"duplex/internal/catalog"
	"duplex/internal/config"
)

// LayoutUpgradeReport describes an explicit offline maintenance run.
type LayoutUpgradeReport struct {
	Scanned  int `json:"scanned"`
	Eligible int `json:"eligible"`
	Upgraded int `json:"upgraded"`
	Skipped  int `json:"skipped"`
}

// UpgradeLayouts previews or applies local version upgrades. It shares the
// service ownership lock, performs no network work, and never starts workers.
func UpgradeLayouts(ctx context.Context, c config.Config, videoID string, apply bool) (report LayoutUpgradeReport, result error) {
	release, err := cache.LockMaintenance(c.CacheDir)
	if err != nil {
		return report, err
	}

	defer func() { result = errors.Join(result, release()) }()

	store, err := catalog.OpenForBackupContext(ctx, filepath.Join(c.DataDir, "catalogue.sqlite"))
	if err != nil {
		return report, err
	}

	defer func() { result = errors.Join(result, store.Close()) }()

	if videoID != "" {
		return upgradeLayoutPage(ctx, c.IndexDir, store, []string{videoID}, apply, report)
	}

	after := ""
	for {
		ids, err := store.LayoutUpgradePage(ctx, after)
		if err != nil {
			return report, err
		}

		if len(ids) == 0 {
			return report, nil
		}

		report, err = upgradeLayoutPage(ctx, c.IndexDir, store, ids, apply, report)
		if err != nil {
			return report, err
		}

		after = ids[len(ids)-1]
	}
}

func upgradeLayoutPage(ctx context.Context, dir string, store *catalog.Store, ids []string, apply bool, report LayoutUpgradeReport) (LayoutUpgradeReport, error) {
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return report, err
		}

		eligible, err := upgradeOneLayout(ctx, dir, store, id, apply)
		if err != nil {
			return report, err
		}

		report.Scanned++
		if !eligible {
			report.Skipped++
			continue
		}

		report.Eligible++
		if apply {
			report.Upgraded++
		}
	}

	return report, nil
}
