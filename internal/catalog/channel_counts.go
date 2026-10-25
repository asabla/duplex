package catalog

import (
	"context"
	"errors"
	"strings"
)

// ChannelCounts aggregates selected recordings by library owner for one page.
// A shared recording is counted once per owning channel, including playlists
// whose uploader differs from the followed channel.
func (s *Store) ChannelCounts(ctx context.Context, ids []string, visible Visibility) (map[string]Counts, error) {
	if len(ids) > 100 {
		return nil, errors.New("channel summary exceeds one page")
	}
	cte, args := selectedCTE(visible)
	cte = strings.TrimSpace(cte) + "," + valueSet("requested", ids, &args) + `,owners(channel,id) AS (
 SELECT v.channel_id,v.id FROM videos v JOIN active_channels c ON c.id=v.channel_id JOIN requested r ON r.id=c.id WHERE v.selected=1
 UNION SELECT p.channel_id,m.video_id FROM playlist_members m JOIN playlists p ON p.id=m.playlist_id JOIN playlist_channels c ON c.id=p.channel_id JOIN requested r ON r.id=p.channel_id WHERE m.selected=1 AND NOT EXISTS(SELECT 1 FROM skipped_playlists x WHERE x.id=p.id)) `
	rows, err := s.DB.QueryContext(ctx, cte+`SELECT o.channel,count(*),COALESCE(sum(v.preparation='ready' AND v.state IN ('recorded','replay-ready')),0),COALESCE(sum(v.preparation='failed'),0) FROM owners o JOIN videos v ON v.id=o.id GROUP BY o.channel`, args...)
	if err != nil {
		return nil, err
	}
	defer closeRows(rows)
	result := map[string]Counts{}
	for rows.Next() {
		var id string
		var c Counts
		if err = rows.Scan(&id, &c.Selected, &c.Ready, &c.Failed); err != nil {
			return nil, err
		}
		result[id] = c
	}
	return result, rows.Err()
}
