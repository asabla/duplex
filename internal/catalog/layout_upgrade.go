package catalog

import (
	"context"
	"errors"
	"time"
)

// LayoutUpgradePage seeks through retained canonical media without changing
// selection, playlist slots, discovery history, or Plex bindings.
func (s *Store) LayoutUpgradePage(ctx context.Context, after string) ([]string, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id FROM videos WHERE id>? AND revision<>'' ORDER BY id LIMIT 128`, after)
	if err != nil {
		return nil, err
	}
	defer closeRows(rows)

	var ids []string

	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}

		ids = append(ids, id)
	}

	return ids, rows.Err()
}

// UpgradeLayoutRevision changes only the current media revision and byte size.
// Retired representations and all occurrence identities remain durable.
func (s *Store) UpgradeLayoutRevision(ctx context.Context, id, old, revision string, size int64) error {
	if old == "" || revision == "" || size <= 0 {
		return errors.New("invalid layout upgrade")
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)

	now := time.Now().Unix()

	_, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO representations(revision,video_id,height,size,created) SELECT ?,id,height,?,? FROM videos WHERE id=? AND revision=?`, revision, size, now, id, old)
	if err != nil {
		return err
	}

	result, err := tx.ExecContext(ctx, `UPDATE videos SET revision=?,size=?,updated=MAX(updated+1,?,COALESCE((SELECT MAX(MAX(m.updated,p.updated))+1 FROM playlist_members m JOIN playlists p ON p.id=m.playlist_id WHERE m.video_id=videos.id),0)) WHERE id=? AND revision=?`, revision, size, now, id, old)
	if err != nil {
		return err
	}

	count, err := result.RowsAffected()
	if err != nil {
		return err
	}

	if count != 1 {
		return errors.New("recording changed during layout upgrade")
	}

	return tx.Commit()
}
