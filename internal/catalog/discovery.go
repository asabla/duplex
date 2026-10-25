package catalog

import (
	"context"
	"database/sql"
	"time"
)

// StageDiscovery checkpoints identities and their first source positions. The
// caller may stop after a bounded window; reconciliation is a separate step.
func (s *Store) StageDiscovery(ctx context.Context, run int64, collection, video string, position int) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO discovery_items(run_id,collection,video_id,position) VALUES(?,?,?,?) ON CONFLICT(run_id,collection,video_id) DO NOTHING`, run, collection, video, position)
	return err
}

// ReconcileDiscovery changes membership only after enumeration has succeeded.
// Latest selects by publication time, then assigns playlist slots in source order.
func (s *Store) ReconcileDiscovery(ctx context.Context, run int64, collection, owner string, limit int) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	selection := `SELECT d.video_id FROM discovery_items d JOIN videos v ON v.id=d.video_id WHERE d.run_id=? AND d.collection=? AND v.state IN ('recorded','replay-ready') ORDER BY v.published DESC,v.id`
	args := []any{run, collection}
	if limit > 0 {
		selection += ` LIMIT ?`
		args = append(args, limit)
	}
	if collection == "uploads" {
		if _, err = tx.ExecContext(ctx, `UPDATE videos SET selected=0,exposed=0 WHERE channel_id=?`, owner); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE videos SET selected=1,exposed=(revision<>'' AND number>0) WHERE id IN (`+selection+`)`, args...)
	} else {
		if _, err = tx.ExecContext(ctx, `UPDATE playlist_members SET selected=0 WHERE playlist_id=?`, collection); err != nil {
			return err
		}
		// UPSERT from SELECT requires an explicit WHERE before ON CONFLICT.
		query := `WITH chosen AS (` + selection + `), ordered AS (SELECT d.video_id,d.position,ROW_NUMBER() OVER(ORDER BY d.position,d.video_id) AS episode FROM discovery_items d JOIN chosen c ON c.video_id=d.video_id WHERE d.run_id=? AND d.collection=?) INSERT INTO playlist_members(playlist_id,video_id,position,selected,episode) SELECT ?,video_id,position,1,episode FROM ordered WHERE true ON CONFLICT(playlist_id,video_id) DO UPDATE SET position=excluded.position,selected=1,episode=excluded.episode`
		args = append(args, run, collection, collection)
		_, err = tx.ExecContext(ctx, query, args...)
	}
	if err != nil {
		return err
	}
	if collection == "uploads" {
		_, err = tx.ExecContext(ctx, `UPDATE channels SET synced_at=?,error='' WHERE id=?`, time.Now().Unix(), owner)
	} else {
		_, err = tx.ExecContext(ctx, `UPDATE playlists SET synced_at=?,error='' WHERE id=?`, time.Now().Unix(), collection)
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

// DiscoveryPosition supports bounded, durable progress reporting.
func (s *Store) DiscoveryPosition(ctx context.Context, run int64, collection string) (int, error) {
	var n int
	err := s.DB.QueryRowContext(ctx, `SELECT COALESCE(MAX(position),0) FROM discovery_items WHERE run_id=? AND collection=?`, run, collection).Scan(&n)
	return n, err
}

// ActiveRevision returns the exact durable representation without its metadata.
func (s *Store) ActiveRevision(ctx context.Context, id string) (string, error) {
	var revision string
	err := s.DB.QueryRowContext(ctx, `SELECT revision FROM videos WHERE id=?`, id).Scan(&revision)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return revision, err
}

// ChooseDiscovery records a canonical selection once even across duplicate tabs.
func (s *Store) ChooseDiscovery(ctx context.Context, run int64, collection, id string) (bool, error) {
	result, err := s.DB.ExecContext(ctx, `UPDATE discovery_items SET chosen=1 WHERE run_id=? AND collection=? AND video_id=? AND chosen=0`, run, collection, id)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n > 0, err
}
