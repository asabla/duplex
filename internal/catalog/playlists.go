package catalog

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"duplex/internal/source"
)

// Playlist is a channel-owned collection with a persistent Plex season number.
type Playlist struct {
	ID          string `json:"id"`
	ChannelID   string `json:"channel_id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Thumbnail   string `json:"thumbnail,omitempty"`
	Season      int    `json:"season"`
	SyncedAt    int64  `json:"synced_at"`
	Error       string `json:"error,omitempty"`
}

// Member retains source ordering independently of a video's uploader.
type Member struct {
	VideoID  string `json:"video_id"`
	Position int    `json:"position"`
}

// SavePlaylist preserves season allocation through title and metadata changes.
func (s *Store) SavePlaylist(ctx context.Context, p Playlist) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	var owner string
	err = tx.QueryRowContext(ctx, `SELECT channel_id,season FROM playlists WHERE id=?`, p.ID).Scan(&owner, &p.Season)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if owner != "" && owner != p.ChannelID {
		return errors.New("playlist owner changed")
	}
	if errors.Is(err, sql.ErrNoRows) {
		if err = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(season),0)+1 FROM playlists WHERE channel_id=?`, p.ChannelID).Scan(&p.Season); err != nil {
			return err
		}
		if p.Season >= 1000 && p.Season <= 9999 {
			p.Season = 10000
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO playlists(id,channel_id,title,description,thumbnail,season) VALUES(?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET title=excluded.title,description=excluded.description,thumbnail=CASE WHEN excluded.thumbnail<>'' THEN excluded.thumbnail ELSE playlists.thumbnail END`, p.ID, p.ChannelID, p.Title, p.Description, p.Thumbnail, p.Season)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// Playlists returns stable collection metadata for one followed channel.
func (s *Store) Playlists(ctx context.Context, channel string) ([]Playlist, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id,channel_id,title,description,thumbnail,season,synced_at,error FROM playlists WHERE channel_id=? ORDER BY title,id`, channel)
	if err != nil {
		return nil, err
	}
	defer closeRows(rows)
	result := []Playlist{}
	for rows.Next() {
		var p Playlist
		if err = rows.Scan(&p.ID, &p.ChannelID, &p.Title, &p.Description, &p.Thumbnail, &p.Season, &p.SyncedAt, &p.Error); err != nil {
			return nil, err
		}
		result = append(result, p)
	}
	return result, rows.Err()
}

// ReplacePlaylistMembers commits only a successfully enumerated selection.
// Failed/partial enumeration must not call this reconciliation boundary.
func (s *Store) ReplacePlaylistMembers(ctx context.Context, id string, members []Member, selected map[string]bool, complete bool) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if _, err = tx.ExecContext(ctx, `UPDATE playlist_members SET selected=0 WHERE playlist_id=?`, id); err != nil {
		return err
	}
	seen := map[string]bool{}
	episode := 0
	for _, m := range members {
		if seen[m.VideoID] {
			continue
		}
		seen[m.VideoID] = true
		number := 0
		if selected[m.VideoID] {
			episode++
			number = episode
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO playlist_members(playlist_id,video_id,position,selected,episode) VALUES(?,?,?,?,?) ON CONFLICT(playlist_id,video_id) DO UPDATE SET position=excluded.position,selected=excluded.selected,episode=excluded.episode`, id, m.VideoID, m.Position, selected[m.VideoID], number)
		if err != nil {
			return err
		}
	}
	if complete {
		rows, e := tx.QueryContext(ctx, `SELECT video_id FROM playlist_members WHERE playlist_id=?`, id)
		if e != nil {
			return e
		}
		obsolete := []string{}
		for rows.Next() {
			var video string
			if e = rows.Scan(&video); e != nil {
				closeRows(rows)
				return e
			}
			if !seen[video] {
				obsolete = append(obsolete, video)
			}
		}
		if e = rows.Err(); e != nil {
			closeRows(rows)
			return e
		}
		closeRows(rows)
		for _, video := range obsolete {
			if _, err = tx.ExecContext(ctx, `DELETE FROM playlist_members WHERE playlist_id=? AND video_id=?`, id, video); err != nil {
				return err
			}
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE playlists SET synced_at=?,error='' WHERE id=?`, time.Now().Unix(), id); err != nil {
		return err
	}
	return tx.Commit()
}

// PlaylistOccurrenceID remains stable when a playlist is renamed or reordered.
func PlaylistOccurrenceID(channel, playlist, video string) string {
	return fmt.Sprintf("youtube-playlist:%s:%s:%s", channel, playlist, video)
}

// Discover adds durable identities without changing existing selection or slots.
func (s *Store) Discover(ctx context.Context, owner string, entries []source.Entry) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	for _, entry := range entries {
		_, err = tx.ExecContext(ctx, `INSERT INTO videos(id,channel_id,title,search_title,state,updated) VALUES(?,?,?,?,'discovered',?) ON CONFLICT(id) DO UPDATE SET title=CASE WHEN excluded.title<>'' THEN excluded.title ELSE videos.title END,search_title=CASE WHEN excluded.title<>'' THEN excluded.search_title ELSE videos.search_title END`, entry.ID, owner, entry.Title, strings.ToLower(entry.Title), time.Now().Unix())
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

func rollback(tx *sql.Tx) {
	// Rollback after Commit returns ErrTxDone. Earlier failures are returned by
	// the operation; SQLite releases the transaction when its connection closes.
	if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
		slog.Error("catalogue rollback failed")
	}
}

func closeRows(rows *sql.Rows) {
	// Iteration errors are checked with Rows.Err; Close only releases the cursor.
	if err := rows.Close(); err != nil {
		slog.Error("catalogue cursor close failed")
	}
}
