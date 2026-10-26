package catalog

import (
	"context"
	"slices"
)

// migrateProjectionTimes prepares directory timestamps when source records
// change. These derived records contain no paths, source locators, or media.
func (s *Store) migrateProjectionTimes(ctx context.Context) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)

	for _, statement := range []string{
		`CREATE TABLE projection_years(channel_id TEXT NOT NULL,year INTEGER NOT NULL,PRIMARY KEY(channel_id,year))`,
		`INSERT INTO projection_years SELECT DISTINCT channel_id,year FROM videos`,
		`CREATE INDEX video_projection_time ON videos(channel_id,year,updated DESC) WHERE selected=1 AND number>0 AND revision<>'' AND state IN ('recorded','replay-ready')`,
		`CREATE TABLE projection_playlist_times(playlist_id TEXT NOT NULL,video_id TEXT NOT NULL,modified INTEGER NOT NULL,PRIMARY KEY(playlist_id,video_id))`,
		`CREATE INDEX projection_playlist_latest ON projection_playlist_times(playlist_id,modified DESC)`,
		`CREATE INDEX projection_playlist_video ON projection_playlist_times(video_id,playlist_id)`,
		`INSERT INTO projection_playlist_times SELECT m.playlist_id,m.video_id,MAX(m.updated,v.updated) FROM playlist_members m JOIN videos v ON v.id=m.video_id WHERE m.selected=1 AND m.episode>0 AND v.revision<>'' AND v.state IN ('recorded','replay-ready')`,
		`CREATE TRIGGER projection_year_insert AFTER INSERT ON videos BEGIN INSERT OR IGNORE INTO projection_years VALUES(NEW.channel_id,NEW.year); END`,
		`CREATE TRIGGER projection_year_update AFTER UPDATE OF channel_id,year ON videos WHEN OLD.channel_id<>NEW.channel_id OR OLD.year<>NEW.year BEGIN INSERT OR IGNORE INTO projection_years VALUES(NEW.channel_id,NEW.year); END`,
		`CREATE TRIGGER projection_time_insert AFTER INSERT ON playlist_members BEGIN INSERT INTO projection_playlist_times SELECT NEW.playlist_id,NEW.video_id,MAX(NEW.updated,v.updated) FROM videos v WHERE v.id=NEW.video_id AND NEW.selected=1 AND NEW.episode>0 AND v.revision<>'' AND v.state IN ('recorded','replay-ready'); END`,
		`CREATE TRIGGER projection_time_update AFTER UPDATE OF playlist_id,video_id,selected,episode,updated ON playlist_members WHEN OLD.playlist_id<>NEW.playlist_id OR OLD.video_id<>NEW.video_id OR OLD.selected<>NEW.selected OR OLD.episode<>NEW.episode OR OLD.updated<>NEW.updated BEGIN DELETE FROM projection_playlist_times WHERE playlist_id=OLD.playlist_id AND video_id=OLD.video_id; INSERT INTO projection_playlist_times SELECT NEW.playlist_id,NEW.video_id,MAX(NEW.updated,v.updated) FROM videos v WHERE v.id=NEW.video_id AND NEW.selected=1 AND NEW.episode>0 AND v.revision<>'' AND v.state IN ('recorded','replay-ready'); END`,
		`CREATE TRIGGER projection_time_delete AFTER DELETE ON playlist_members BEGIN DELETE FROM projection_playlist_times WHERE playlist_id=OLD.playlist_id AND video_id=OLD.video_id; END`,
		`CREATE TRIGGER projection_time_media AFTER UPDATE OF id,state,revision,updated ON videos WHEN OLD.id<>NEW.id OR OLD.state<>NEW.state OR OLD.revision<>NEW.revision OR OLD.updated<>NEW.updated BEGIN DELETE FROM projection_playlist_times WHERE video_id=OLD.id; INSERT INTO projection_playlist_times SELECT m.playlist_id,NEW.id,MAX(m.updated,NEW.updated) FROM playlist_members m WHERE m.video_id=NEW.id AND m.selected=1 AND m.episode>0 AND NEW.revision<>'' AND NEW.state IN ('recorded','replay-ready'); END`,
		`PRAGMA user_version=6`,
	} {
		if _, err = tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}

	return tx.Commit()
}

// projectionSeasonsCTE reads one indexed maximum per collection. Visibility is
// applied at read time so YAML changes do not depend on a projection rebuild.
// Fix the upload index: SQLite can otherwise prefer the episode-number range
// index and sort every recording before it returns the latest timestamp.
func projectionSeasonsCTE(v Visibility, owner string) (string, []any) {
	args := []any{}
	skipped := valueSet("skipped_playlists", v.SkippedPlaylists, &args)
	args = append(args, owner, slices.Contains(v.Channels, owner), owner, slices.Contains(v.PlaylistChannels, owner), owner, owner)

	return "WITH " + skipped + `,
upload_times AS (
 SELECT y.year,(SELECT v.updated FROM videos v INDEXED BY video_projection_time WHERE v.channel_id=y.channel_id AND v.year=y.year AND v.selected=1 AND v.number>0 AND v.revision<>'' AND v.state IN ('recorded','replay-ready') ORDER BY v.updated DESC LIMIT 1) AS updated
 FROM projection_years y WHERE y.channel_id=? AND ?),
playlist_times AS (
 SELECT p.id,p.title,p.season,p.updated,(SELECT t.modified FROM projection_playlist_times t WHERE t.playlist_id=p.id ORDER BY t.modified DESC LIMIT 1) AS member_updated
 FROM playlists p WHERE p.channel_id=? AND ? AND NOT EXISTS(SELECT 1 FROM skipped_playlists x WHERE x.id=p.id)),
seasons(scope,title,number,modified) AS (
 SELECT 'year:'||u.year,'',u.year,MAX(u.updated,c.updated) FROM upload_times u JOIN channels c ON c.id=? WHERE u.updated IS NOT NULL
 UNION ALL SELECT 'playlist:'||p.id,p.title,p.season,MAX(p.member_updated,p.updated,c.updated) FROM playlist_times p JOIN channels c ON c.id=? WHERE p.member_updated IS NOT NULL) `, args
}
