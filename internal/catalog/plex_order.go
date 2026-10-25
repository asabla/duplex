package catalog

import (
	"context"
	"errors"
	"strings"
)

// ObservedEpisode is one bounded Plex observation, before any index mutations.
type ObservedEpisode struct {
	Occurrence, Key, GUID string
	Index                 int
}

// ObserveEpisodes saves a complete page in one transaction. A repeated identity
// in the same observation run is rejected before the integration can mutate Plex.
func (s *Store) ObserveEpisodes(ctx context.Context, server string, run int64, episodes []ObservedEpisode) error {
	if len(episodes) > 100 {
		return errors.New("plex observation page exceeds its bound")
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)

	for _, episode := range episodes {
		if episode.Index < 1 || episode.Key == "" {
			return errors.New("plex returned an invalid episode binding")
		}

		owner, playlist, video, err := observedIdentity(episode.Occurrence)
		if err != nil {
			return err
		}

		result, err := tx.ExecContext(ctx, `INSERT INTO episode_bindings(occurrence_id,server,rating_key,guid,owner_id,playlist_id,video_id,current_index,seen_run) VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(occurrence_id,server) DO UPDATE SET rating_key=excluded.rating_key,guid=excluded.guid,owner_id=excluded.owner_id,playlist_id=excluded.playlist_id,video_id=excluded.video_id,current_index=excluded.current_index,seen_run=excluded.seen_run WHERE episode_bindings.seen_run<>excluded.seen_run`, episode.Occurrence, server, episode.Key, episode.GUID, owner, playlist, video, episode.Index, run)
		if err != nil {
			return err
		}

		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}

		if affected != 1 {
			return errors.New("plex returned duplicate occurrence identities")
		}

		if playlist == "" {
			if _, err = tx.ExecContext(ctx, `INSERT INTO plex_bindings(video_id,server,rating_key,guid) VALUES(?,?,?,?) ON CONFLICT(video_id,server) DO UPDATE SET rating_key=excluded.rating_key,guid=excluded.guid`, video, server, episode.Key, episode.GUID); err != nil {
				return err
			}
		}
	}

	return tx.Commit()
}

func observedIdentity(value string) (owner, playlist, video string, err error) {
	if len(value) > 512 {
		return "", "", "", errors.New("invalid Plex occurrence identity")
	}

	parts := strings.Split(value, ":")
	if len(parts) == 2 && parts[0] == "youtube-video" && parts[1] != "" {
		return "", "", parts[1], nil
	}

	if len(parts) == 4 && parts[0] == "youtube-playlist" && parts[1] != "" && parts[2] != "" && parts[3] != "" {
		return parts[1], parts[2], parts[3], nil
	}

	return "", "", "", errors.New("invalid Plex occurrence identity")
}

// EpisodeOrder is an idempotent change based on a durable, fully read snapshot.
type EpisodeOrder struct {
	Occurrence string
	Key        string
	Current    int
	Desired    int
	Temporary  int
}

// EpisodeOrderPage uses catalogue keysets, so changing Plex indexes cannot shift
// a later Plex response page. Vacating old slots also covers removed members.
func (s *Store) EpisodeOrderPage(ctx context.Context, visible Visibility, server string, run int64, promote bool, after string, limit int) ([]EpisodeOrder, error) {
	if limit < 1 || limit > 128 {
		return nil, errors.New("invalid episode order page size")
	}

	args := []any{}
	cte := "WITH " + valueSet("active", visible.PlaylistChannels, &args) + "," + valueSet("skipped", visible.SkippedPlaylists, &args) + `,orders AS (SELECT b.occurrence_id,b.rating_key,b.current_index,CASE WHEN m.selected=1 AND v.revision<>'' AND v.state IN ('recorded','replay-ready') THEN m.episode ELSE 0 END AS desired,1073741824+m.file_episode AS temporary FROM episode_bindings b JOIN playlist_members m ON m.playlist_id=b.playlist_id AND m.video_id=b.video_id JOIN playlists p ON p.id=m.playlist_id AND p.channel_id=b.owner_id JOIN active a ON a.id=p.channel_id JOIN videos v ON v.id=m.video_id WHERE b.server=? AND b.seen_run=? AND NOT EXISTS(SELECT 1 FROM skipped x WHERE x.id=p.id)) `

	where := `current_index<>desired AND current_index<>temporary`
	if promote {
		where = `desired>0 AND current_index<>desired`
	}

	args = append(args, server, run, after, limit)

	rows, err := s.DB.QueryContext(ctx, cte+`SELECT occurrence_id,rating_key,current_index,desired,temporary FROM orders WHERE `+where+` AND occurrence_id>? ORDER BY occurrence_id LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer closeRows(rows)

	result := []EpisodeOrder{}

	for rows.Next() {
		var order EpisodeOrder
		if err = rows.Scan(&order.Occurrence, &order.Key, &order.Current, &order.Desired, &order.Temporary); err != nil {
			return nil, err
		}

		result = append(result, order)
	}

	return result, rows.Err()
}

// AppliedEpisodeOrder checkpoints a successful Plex mutation for retries.
func (s *Store) AppliedEpisodeOrder(ctx context.Context, server string, run int64, occurrence string, index int) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE episode_bindings SET current_index=? WHERE server=? AND seen_run=? AND occurrence_id=?`, index, server, run, occurrence)
	return err
}

// Bind retains a year episode Plex key independently of its source identity.
func (s *Store) Bind(ctx context.Context, id, server, key, guid string) error {
	_, e := s.DB.ExecContext(ctx, `INSERT INTO plex_bindings VALUES(?,?,?,?) ON CONFLICT(video_id,server) DO UPDATE SET rating_key=excluded.rating_key,guid=excluded.guid`, id, server, key, guid)
	return e
}
