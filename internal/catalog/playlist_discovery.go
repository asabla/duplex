package catalog

import (
	"context"
	"database/sql"
	"errors"
)

// FailPlaylist records a redacted failure without changing membership or the
// timestamp of the last complete snapshot. Successful reconciliation clears it.
func (s *Store) FailPlaylist(ctx context.Context, id string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE playlists SET error=? WHERE id=?`, "playlist refresh failed; previous complete membership retained", id)
	return err
}

// DiscoverPlaylist records a safe addition before root enumeration completes.
func (s *Store) DiscoverPlaylist(ctx context.Context, run int64, playlist Playlist) error {
	if err := s.SavePlaylist(ctx, playlist); err != nil {
		return err
	}
	_, err := s.DB.ExecContext(ctx, `UPDATE playlists SET seen_run=?,available=1 WHERE id=?`, run, playlist.ID)
	return err
}

// ReconcilePlaylists removes only exposure, after successful root enumeration.
// Metadata and assigned season numbers remain durable and are never reused.
func (s *Store) ReconcilePlaylists(ctx context.Context, run int64, owner string) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if _, err = tx.ExecContext(ctx, `UPDATE playlist_members SET selected=0 WHERE playlist_id IN (SELECT id FROM playlists WHERE channel_id=? AND seen_run<>?)`, owner, run); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE playlists SET available=(seen_run=?) WHERE channel_id=?`, run, owner); err != nil {
		return err
	}
	return tx.Commit()
}

// DiscoveredPlaylists reads a root snapshot by stable ID, with a fixed page bound.
func (s *Store) DiscoveredPlaylists(ctx context.Context, run int64, owner, after string, limit int) ([]Playlist, error) {
	if limit < 1 || limit > 100 {
		return nil, errors.New("invalid playlist discovery page size")
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT id,channel_id,title,description,thumbnail,season,synced_at,error,available FROM playlists WHERE seen_run=? AND channel_id=? AND id>? ORDER BY id LIMIT ?`, run, owner, after, limit)
	if err != nil {
		return nil, err
	}
	return readPlaylistMetadata(rows)
}

func readPlaylistMetadata(rows *sql.Rows) ([]Playlist, error) {
	defer closeRows(rows)
	out := []Playlist{}
	for rows.Next() {
		var p Playlist
		if err := rows.Scan(&p.ID, &p.ChannelID, &p.Title, &p.Description, &p.Thumbnail, &p.Season, &p.SyncedAt, &p.Error, &p.Available); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
