package catalog

import "context"

// TouchProjection advances retained directory times when YAML hides or returns
// collections. Recording rows, media identities, and Plex bindings are retained.
func (s *Store) TouchProjection(ctx context.Context, channels []string) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)

	for _, id := range channels {
		_, err = tx.ExecContext(ctx, `UPDATE channels SET updated=MAX(updated+1,unixepoch(),COALESCE((SELECT v.updated+1 FROM videos v WHERE v.channel_id=channels.id ORDER BY v.updated DESC LIMIT 1),0),COALESCE((SELECT MAX(MAX(p.updated,COALESCE((SELECT t.modified FROM projection_playlist_times t WHERE t.playlist_id=p.id ORDER BY t.modified DESC LIMIT 1),0)))+1 FROM playlists p WHERE p.channel_id=channels.id),0)) WHERE id=?`, id)
		if err != nil {
			return err
		}
	}

	return tx.Commit()
}

// Directory timestamps must advance when a visible upload leaves or returns,
// even when the recording's own bytes, timestamp, and identity are unchanged.
func (s *Store) migrateUploadTimes(ctx context.Context) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)

	old := `OLD.selected=1 AND OLD.number>0 AND OLD.revision<>'' AND OLD.state IN ('recorded','replay-ready')`
	current := `NEW.selected=1 AND NEW.number>0 AND NEW.revision<>'' AND NEW.state IN ('recorded','replay-ready')`
	changed := `(` + old + `)<>(` + current + `) OR ((OLD.channel_id<>NEW.channel_id OR OLD.year<>NEW.year OR OLD.updated<>NEW.updated) AND ((` + old + `) OR (` + current + `)))`

	for _, query := range []string{
		`CREATE INDEX IF NOT EXISTS video_channel_modified ON videos(channel_id,updated DESC)`,
		`CREATE TRIGGER IF NOT EXISTS upload_directory_time AFTER UPDATE OF selected,revision,state,number,channel_id,year,updated ON videos WHEN ` + changed + ` BEGIN UPDATE channels SET updated=MAX(updated+1,unixepoch(),OLD.updated+1,NEW.updated+1,COALESCE((SELECT v.updated+1 FROM videos v WHERE v.channel_id=channels.id ORDER BY v.updated DESC LIMIT 1),0)) WHERE id IN (OLD.channel_id,NEW.channel_id); END`,
		`PRAGMA user_version=8`,
	} {
		if _, err = tx.ExecContext(ctx, query); err != nil {
			return err
		}
	}

	return tx.Commit()
}
