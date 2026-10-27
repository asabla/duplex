package catalog

import "context"

// RecordingKind prefers completed-broadcast metadata over a duplicated tab entry.
func (s *Store) RecordingKind(ctx context.Context, id string) (string, error) {
	var kind string

	err := s.projectionReader().QueryRowContext(ctx, `SELECT CASE WHEN v.state='replay-ready' THEN 'replay' ELSE COALESCE(k.kind,'') END FROM videos v LEFT JOIN video_content k ON k.video_id=v.id WHERE v.id=?`, id).Scan(&kind)

	return kind, err
}

func (s *Store) migrateContentKinds(ctx context.Context) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)

	for _, query := range []string{
		`CREATE TABLE IF NOT EXISTS video_content(video_id TEXT PRIMARY KEY REFERENCES videos(id),kind TEXT NOT NULL CHECK(kind IN ('upload','short','replay')))`,
		`INSERT OR IGNORE INTO video_content SELECT id,'replay' FROM videos WHERE state='replay-ready'`,
		`CREATE TABLE IF NOT EXISTS upload_rule_state(channel_id TEXT PRIMARY KEY REFERENCES channels(id),digest TEXT NOT NULL)`,
		`PRAGMA user_version=9`,
	} {
		if _, err = tx.ExecContext(ctx, query); err != nil {
			return err
		}
	}

	return tx.Commit()
}
