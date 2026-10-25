package catalog

import (
	"context"
	"database/sql"
)

// HintRevision ties a text artifact to a durable catalogue and collection revision.
type HintRevision struct {
	Database string `json:"database"`
	Playlist string `json:"playlist"`
	Revision int64  `json:"revision"`
}

// FileHint links an immutable scanner slot to its current display episode index.
type FileHint struct {
	Number     int
	FileNumber int
}

// HintVersion does not hydrate the membership or media catalogue.
func (s *Store) HintVersion(ctx context.Context, channel string, season int) (HintRevision, error) {
	return hintVersion(ctx, s.DB, channel, season)
}

type hintQuery interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func hintVersion(ctx context.Context, query hintQuery, channel string, season int) (HintRevision, error) {
	var result HintRevision

	err := query.QueryRowContext(ctx, `SELECT c.identity,p.id,p.projection_revision FROM playlists p CROSS JOIN catalogue_identity c WHERE p.channel_id=? AND p.season=?`, channel, season).Scan(&result.Database, &result.Playlist, &result.Revision)

	return result, err
}

// WalkHints reads one consistent snapshot in fixed pages. Only the writer runs
// inside this local transaction; no network request or Plex read holds it open.
func (s *Store) WalkHints(ctx context.Context, channel string, season int, consume func([]FileHint) error) (HintRevision, error) {
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return HintRevision{}, err
	}
	defer rollback(tx)

	version, err := hintVersion(ctx, tx, channel, season)
	if err != nil {
		return version, err
	}

	after := 0
	for {
		rows, err := tx.QueryContext(ctx, `SELECT m.episode,m.file_episode FROM playlist_members m JOIN videos v ON v.id=m.video_id WHERE m.playlist_id=? AND m.selected=1 AND m.episode>? AND v.revision<>'' AND v.state IN ('recorded','replay-ready') ORDER BY m.episode,m.video_id LIMIT 128`, version.Playlist, after)
		if err != nil {
			return version, err
		}

		hints, err := readHints(rows)
		if err != nil {
			return version, err
		}

		if err = consume(hints); err != nil {
			return version, err
		}

		if len(hints) < 128 {
			return version, tx.Commit()
		}

		after = hints[len(hints)-1].Number
	}
}

func readHints(rows *sql.Rows) ([]FileHint, error) {
	defer closeRows(rows)

	result := []FileHint{}

	for rows.Next() {
		var hint FileHint
		if err := rows.Scan(&hint.Number, &hint.FileNumber); err != nil {
			return nil, err
		}

		result = append(result, hint)
	}

	return result, rows.Err()
}
