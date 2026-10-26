package catalog

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// ensureProjectionIndexes adds derived read indexes without changing stored
// identities or the schema version understood by existing installations.
func (s *Store) ensureProjectionIndexes(ctx context.Context) error {
	_, err := s.DB.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS video_projection_name ON videos(channel_id,year,printf('%04d',number),id) WHERE selected=1 AND number>0 AND revision<>'' AND state IN ('recorded','replay-ready');
CREATE INDEX IF NOT EXISTS playlist_projection_name ON playlist_members(playlist_id,printf('%04d',file_episode),video_id) WHERE selected=1 AND episode>0;`)

	return err
}

// ProjectionEntries seeks through indexed lexical file numbers. Year seasons
// occupy 1000-9999; playlist season allocation excludes that reserved range.
func (s *Store) ProjectionEntries(ctx context.Context, v Visibility, owner string, season int, after string, limit int) ([]ProjectionEntry, error) {
	if limit < 1 || limit > 256 {
		return nil, errors.New("invalid projection page size")
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	cursor, possible := projectionNumberCursor(owner, season, after)

	query, args := projectionEntriesQuery(v, owner, season, cursor, limit)
	if !possible || query == "" {
		return []ProjectionEntry{}, nil
	}

	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer closeRows(rows)

	result := []ProjectionEntry{}

	for rows.Next() {
		var entry ProjectionEntry
		if err := rows.Scan(&entry.VideoID, &entry.PlaylistID, &entry.FileNumber); err != nil {
			return nil, err
		}

		result = append(result, entry)
	}

	return result, rows.Err()
}

func projectionNumberCursor(owner string, season int, after string) (string, bool) {
	prefix := fmt.Sprintf("Channel-%s - S%04dE", owner, season)
	if after < prefix {
		return "", true
	}

	return strings.CutPrefix(after, prefix)
}

func projectionEntriesQuery(v Visibility, owner string, season int, cursor string, limit int) (string, []any) {
	if season >= 1000 && season <= 9999 {
		if !slices.Contains(v.Channels, owner) {
			return "", nil
		}

		return `SELECT id,'',number FROM videos WHERE channel_id=? AND year=? AND selected=1 AND number>0 AND revision<>'' AND state IN ('recorded','replay-ready') AND printf('%04d',number)>=? ORDER BY printf('%04d',number),id LIMIT ?`, []any{owner, season, cursor, limit}
	}

	if !slices.Contains(v.PlaylistChannels, owner) {
		return "", nil
	}

	args := []any{}
	skipped := valueSet("skipped_playlists", v.SkippedPlaylists, &args)
	args = append(args, owner, season, cursor, limit)

	return "WITH " + skipped + ` SELECT m.video_id,m.playlist_id,m.file_episode FROM playlist_members m JOIN videos v ON v.id=m.video_id WHERE m.playlist_id=(SELECT p.id FROM playlists p WHERE p.channel_id=? AND p.season=? AND NOT EXISTS(SELECT 1 FROM skipped_playlists x WHERE x.id=p.id)) AND m.selected=1 AND m.episode>0 AND v.revision<>'' AND v.state IN ('recorded','replay-ready') AND printf('%04d',m.file_episode)>=? ORDER BY printf('%04d',m.file_episode),m.video_id LIMIT ?`, args
}
