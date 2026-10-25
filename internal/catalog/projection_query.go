package catalog

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

func projectionCTE(v Visibility) (string, []any) {
	args := []any{}
	sets := []string{valueSet("active_channels", v.Channels, &args), valueSet("playlist_channels", v.PlaylistChannels, &args), valueSet("skipped_playlists", v.SkippedPlaylists, &args)}
	sets = append(sets, `episodes(owner,title,video,name,description,playlist,season_title,season,number,revision,size,updated) AS (
 SELECT v.channel_id,c.title,v.id,v.title,v.description,'','',v.year,v.number,v.revision,v.size,MAX(v.updated,c.updated) FROM videos v JOIN channels c ON c.id=v.channel_id JOIN active_channels a ON a.id=v.channel_id WHERE v.selected=1 AND v.revision<>'' AND v.number>0 AND v.state IN ('recorded','replay-ready')
 UNION ALL SELECT p.channel_id,c.title,v.id,v.title,v.description,p.id,p.title,p.season,m.episode,v.revision,v.size,MAX(v.updated,m.updated,p.updated,c.updated) FROM playlist_members m JOIN playlists p ON p.id=m.playlist_id JOIN channels c ON c.id=p.channel_id JOIN playlist_channels a ON a.id=p.channel_id JOIN videos v ON v.id=m.video_id WHERE m.selected=1 AND m.episode>0 AND v.revision<>'' AND v.state IN ('recorded','replay-ready') AND NOT EXISTS(SELECT 1 FROM skipped_playlists x WHERE x.id=p.id))`)
	return "WITH " + strings.Join(sets, ",") + " ", args
}

// ProjectionPage returns a bounded season page without loading media indexes.
func (s *Store) ProjectionPage(ctx context.Context, v Visibility, owner string, season, after, limit int) ([]Episode, error) {
	if limit < 1 || limit > 256 {
		return nil, errors.New("invalid projection page size")
	}
	cte, args := projectionCTE(v)
	args = append(args, owner, season, after, limit)
	rows, err := s.DB.QueryContext(ctx, cte+`SELECT owner,title,video,name,description,playlist,season_title,season,number,revision,size,updated FROM episodes WHERE owner=? AND season=? AND number>? ORDER BY number,video LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	return readProjectionRows(rows)
}

// ProjectionSeason is one visible directory and its local poster identity.
type ProjectionSeason struct {
	Name     string
	Scope    string
	Title    string
	Number   int
	Modified int64
}

// ProjectionSeasons pages by the exact mounted directory name.
func (s *Store) ProjectionSeasons(ctx context.Context, v Visibility, owner, after string, limit int) ([]ProjectionSeason, error) {
	if limit < 1 || limit > 256 {
		return nil, errors.New("invalid projection page size")
	}
	cte, args := projectionCTE(v)
	args = append(args, owner, after, limit)
	rows, err := s.DB.QueryContext(ctx, cte+`SELECT printf('Season %04d',season),CASE WHEN playlist='' THEN 'year:'||season ELSE 'playlist:'||playlist END,season_title,season,MAX(updated) FROM episodes WHERE owner=? GROUP BY season HAVING printf('Season %04d',season)>? ORDER BY printf('Season %04d',season) LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	return readProjectionSeasons(rows)
}

// ProjectionChanged returns the latest visible metadata change for a show.
func (s *Store) ProjectionChanged(ctx context.Context, v Visibility, owner string) (int64, error) {
	cte, args := projectionCTE(v)
	args = append(args, owner)
	var changed sql.NullInt64
	err := s.DB.QueryRowContext(ctx, cte+`SELECT MAX(updated) FROM episodes WHERE owner=?`, args...).Scan(&changed)
	if err == nil && !changed.Valid {
		err = sql.ErrNoRows
	}
	return changed.Int64, err
}

// ProjectionFilesPage uses the exact mounted base name for lexical keyset paging.
func (s *Store) ProjectionFilesPage(ctx context.Context, v Visibility, owner string, season int, after string, limit int) ([]Episode, error) {
	if limit < 1 || limit > 256 {
		return nil, errors.New("invalid projection page size")
	}
	cte, args := projectionCTE(v)
	args = append(args, owner, season, after, limit)
	rows, err := s.DB.QueryContext(ctx, cte+`SELECT owner,title,video,name,description,playlist,season_title,season,number,revision,size,updated FROM episodes WHERE owner=? AND season=? AND printf('Channel-%s - S%04dE%04d',owner,season,number)>=? ORDER BY printf('Channel-%s - S%04dE%04d',owner,season,number),video LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	return readProjectionRows(rows)
}

// ProjectionPosters pages season artwork names without loading all seasons.
func (s *Store) ProjectionPosters(ctx context.Context, v Visibility, owner, after string, limit int) ([]ProjectionSeason, error) {
	if limit < 1 || limit > 256 {
		return nil, errors.New("invalid projection page size")
	}
	cte, args := projectionCTE(v)
	args = append(args, owner, after, limit)
	rows, err := s.DB.QueryContext(ctx, cte+`SELECT printf('season%02d-poster.jpg',season),CASE WHEN playlist='' THEN 'year:'||season ELSE 'playlist:'||playlist END,season_title,season,MAX(updated) FROM episodes WHERE owner=? GROUP BY season HAVING printf('season%02d-poster.jpg',season)>? ORDER BY printf('season%02d-poster.jpg',season) LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	return readProjectionSeasons(rows)
}

// ProjectionSeasonAt reads one visible season and its stable artwork scope.
func (s *Store) ProjectionSeasonAt(ctx context.Context, v Visibility, owner string, number int) (ProjectionSeason, error) {
	cte, args := projectionCTE(v)
	args = append(args, owner, number)
	var season ProjectionSeason
	err := s.DB.QueryRowContext(ctx, cte+`SELECT printf('Season %04d',season),CASE WHEN playlist='' THEN 'year:'||season ELSE 'playlist:'||playlist END,season_title,season,MAX(updated) FROM episodes WHERE owner=? AND season=? GROUP BY season`, args...).Scan(&season.Name, &season.Scope, &season.Title, &season.Number, &season.Modified)
	return season, err
}

// HasSeason checks a durable artwork scope, including unavailable recordings.
func (s *Store) HasSeason(ctx context.Context, owner, scope string) (bool, error) {
	var exists bool
	err := s.DB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM videos WHERE channel_id=? AND year BETWEEN 1000 AND 9999 AND 'year:'||year=? UNION ALL SELECT 1 FROM playlists WHERE channel_id=? AND 'playlist:'||id=?)`, owner, scope, owner, scope).Scan(&exists)
	return exists, err
}

func readProjectionSeasons(rows *sql.Rows) ([]ProjectionSeason, error) {
	defer closeRows(rows)
	result := []ProjectionSeason{}
	for rows.Next() {
		var season ProjectionSeason
		if err := rows.Scan(&season.Name, &season.Scope, &season.Title, &season.Number, &season.Modified); err != nil {
			return nil, err
		}
		result = append(result, season)
	}
	return result, rows.Err()
}
