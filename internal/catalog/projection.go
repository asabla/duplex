package catalog

import (
	"context"
	"database/sql"
)

// Episode is one library occurrence of canonical media. Playlist occurrences
// have their own Plex identity while sharing the same representation revision.
type Episode struct {
	OwnerID      string
	ChannelTitle string
	VideoID      string
	Title        string
	Description  string
	PlaylistID   string
	SeasonTitle  string
	OccurrenceID string
	Season       int
	Number       int
	Revision     string
	Size         int64
	Updated      int64
}

// Episodes reads only selected library occurrences, not the whole catalogue.
func (s *Store) Episodes(ctx context.Context) ([]Episode, error) {
	query := `SELECT v.channel_id,c.title,v.id,v.title,v.description,'','',v.year,v.number,v.revision,v.size,v.updated FROM videos v JOIN channels c ON c.id=v.channel_id WHERE v.selected=1 AND v.revision<>'' AND v.number>0 AND v.state IN ('recorded','replay-ready')
UNION ALL SELECT p.channel_id,c.title,v.id,v.title,v.description,p.id,p.title,p.season,m.episode,v.revision,v.size,MAX(v.updated,m.updated,p.updated) FROM playlist_members m JOIN playlists p ON p.id=m.playlist_id JOIN channels c ON c.id=p.channel_id JOIN videos v ON v.id=m.video_id WHERE m.selected=1 AND m.episode>0 AND v.revision<>'' AND v.state IN ('recorded','replay-ready')
ORDER BY 1,8,9,3`
	rows, err := s.DB.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	return readProjectionRows(rows)
}

// BindOccurrence retains Plex identity independently of canonical source IDs.
func (s *Store) BindOccurrence(ctx context.Context, occurrence, server, key, guid string) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO episode_bindings VALUES(?,?,?,?) ON CONFLICT(occurrence_id,server) DO UPDATE SET rating_key=excluded.rating_key,guid=excluded.guid`, occurrence, server, key, guid)
	return err
}

// ChannelArtwork retains the last successfully discovered branding URLs.
func (s *Store) ChannelArtwork(ctx context.Context, id, avatar, banner string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE channels SET avatar=CASE WHEN ?<>'' THEN ? ELSE avatar END,banner=CASE WHEN ?<>'' THEN ? ELSE banner END WHERE id=?`, avatar, avatar, banner, banner, id)
	return err
}

// Branding returns only the channel's public metadata image candidates.
func (s *Store) Branding(ctx context.Context, id string) (title, avatar, banner string, err error) {
	err = s.DB.QueryRowContext(ctx, `SELECT title,avatar,banner FROM channels WHERE id=?`, id).Scan(&title, &avatar, &banner)
	return title, avatar, banner, err
}

// Seasons returns durable season scopes for artwork, including temporarily
// unavailable recordings. It does not require a playable representation.
func (s *Store) Seasons(ctx context.Context, id string) (map[string]int, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT 'year:'||year,year FROM videos WHERE channel_id=? AND year BETWEEN 1000 AND 9999 GROUP BY year UNION ALL SELECT 'playlist:'||id,season FROM playlists WHERE channel_id=?`, id, id)
	if err != nil {
		return nil, err
	}
	defer closeRows(rows)
	out := map[string]int{}
	for rows.Next() {
		var scope string
		var season int
		if err := rows.Scan(&scope, &season); err != nil {
			return nil, err
		}
		out[scope] = season
	}
	return out, rows.Err()
}

func readProjectionRows(rows *sql.Rows) ([]Episode, error) {
	defer closeRows(rows)
	result := []Episode{}
	for rows.Next() {
		var e Episode
		if err := rows.Scan(&e.OwnerID, &e.ChannelTitle, &e.VideoID, &e.Title, &e.Description, &e.PlaylistID, &e.SeasonTitle, &e.Season, &e.Number, &e.Revision, &e.Size, &e.Updated); err != nil {
			return nil, err
		}
		e.OccurrenceID = "youtube-video:" + e.VideoID
		if e.PlaylistID != "" {
			e.OccurrenceID = PlaylistOccurrenceID(e.OwnerID, e.PlaylistID, e.VideoID)
		}
		result = append(result, e)
	}
	return result, rows.Err()
}
