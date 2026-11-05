package catalog

import (
	"context"
	"database/sql"
	"errors"
	"slices"
)

// ProjectionMedia resolves only the immutable media attributes required for
// Stat and lazy opens. Titles, descriptions, and NFO bytes are not needed.
func (s *Store) ProjectionMedia(ctx context.Context, v Visibility, owner string, season, number int) (Episode, error) {
	if err := ctx.Err(); err != nil {
		return Episode{}, err
	}

	query, args := projectionFilesQuery(v, owner, season, true, true, number, 1)
	if query == "" {
		return Episode{}, sql.ErrNoRows
	}

	var ep Episode

	err := s.scanProjectionRow(ctx, query, args, &ep.VideoID, &ep.Revision, &ep.Size, &ep.Updated)

	return ep, err
}

// ProjectionEntryDetails returns one lexical page with the metadata needed for
// directory attributes. The scanner slot remains distinct from display order.
func (s *Store) ProjectionEntryDetails(ctx context.Context, v Visibility, owner string, season int, after string, limit int) ([]Episode, error) {
	if limit < 1 || limit > 256 {
		return nil, errors.New("invalid projection page size")
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	cursor, possible := projectionNumberCursor(owner, season, after)

	query, args := projectionFilesQuery(v, owner, season, false, false, cursor, limit)
	if !possible || query == "" {
		return []Episode{}, nil
	}

	rows, err := s.queryProjectionRows(ctx, query, args...)
	if err != nil {
		return nil, err
	}

	return readProjectionRows(rows)
}

func projectionFilesQuery(v Visibility, owner string, season int, attributes, exact bool, key any, limit int) (string, []any) {
	prefix := ""
	args := []any{}
	fields := `v.channel_id,c.title,v.id,v.title,v.description,'','',v.year,v.number,v.number,v.revision,v.size,MAX(v.updated,c.updated)`
	from := `videos v INDEXED BY video_projection_name JOIN channels c ON c.id=v.channel_id WHERE v.channel_id=? AND v.year=? AND v.selected=1 AND v.number>0`
	slot, updated := "v.number", "MAX(v.updated,c.updated)"
	orderID := "v.id"

	if season >= 1000 && season <= 9999 {
		if !slices.Contains(v.Channels, owner) {
			return "", nil
		}
	} else {
		if !slices.Contains(v.PlaylistChannels, owner) {
			return "", nil
		}

		prefix = "WITH " + valueSet("skipped_playlists", v.SkippedPlaylists, &args) + " "
		fields = `p.channel_id,c.title,v.id,v.title,v.description,p.id,p.title,p.season,m.episode,m.file_episode,v.revision,v.size,MAX(v.updated,m.updated,p.updated,c.updated)`
		from = `playlist_members m INDEXED BY playlist_projection_name JOIN playlists p ON p.id=m.playlist_id JOIN channels c ON c.id=p.channel_id JOIN videos v ON v.id=m.video_id WHERE p.channel_id=? AND p.season=? AND m.selected=1 AND m.episode>0 AND NOT EXISTS(SELECT 1 FROM skipped_playlists x WHERE x.id=p.id)`
		slot, updated = "m.file_episode", "MAX(v.updated,m.updated,p.updated,c.updated)"
		orderID = "m.video_id"
	}

	if attributes {
		fields = "v.id,v.revision,v.size," + updated
	}

	query := prefix + "SELECT " + fields + " FROM " + from + ` AND v.revision<>'' AND v.state IN ('recorded','replay-ready')`

	args = append(args, owner, season, key)

	if exact {
		query += " AND printf('%04d'," + slot + ")=printf('%04d',?)"
	} else {
		query += " AND printf('%04d'," + slot + ")>=? ORDER BY printf('%04d'," + slot + ")," + orderID
	}

	return query + " LIMIT ?", append(args, limit)
}
