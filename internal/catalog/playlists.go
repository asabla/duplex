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
	Available   bool   `json:"available"`
	ChannelID   string `json:"channel_id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Thumbnail   string `json:"thumbnail,omitempty"`
	Season      int    `json:"season"`
	SyncedAt    int64  `json:"synced_at"`
	Error       string `json:"error,omitempty"`
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

	_, err = tx.ExecContext(ctx, `INSERT INTO playlists(id,channel_id,title,description,thumbnail,season,updated) VALUES(?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET updated=CASE WHEN playlists.title<>excluded.title OR playlists.description<>excluded.description OR (excluded.thumbnail<>'' AND playlists.thumbnail<>excluded.thumbnail) THEN MAX(playlists.updated+1,excluded.updated,COALESCE((SELECT MAX(MAX(v.updated,m.updated))+1 FROM playlist_members m JOIN videos v ON v.id=m.video_id WHERE m.playlist_id=playlists.id),0)) ELSE playlists.updated END,title=excluded.title,description=excluded.description,thumbnail=CASE WHEN excluded.thumbnail<>'' THEN excluded.thumbnail ELSE playlists.thumbnail END`, p.ID, p.ChannelID, p.Title, p.Description, p.Thumbnail, p.Season, time.Now().Unix())
	if err != nil {
		return err
	}

	return tx.Commit()
}

// Playlists returns stable collection metadata for one followed channel.
func (s *Store) Playlists(ctx context.Context, channel string) ([]Playlist, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id,channel_id,title,description,thumbnail,season,synced_at,error,available FROM playlists WHERE channel_id=? ORDER BY title,id`, channel)
	if err != nil {
		return nil, err
	}

	return readPlaylistMetadata(rows)
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
