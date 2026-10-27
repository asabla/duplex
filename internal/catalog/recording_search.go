package catalog

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"

	"duplex/internal/config"
)

// RecordingSearch is a bounded, channel-owned catalogue lookup for rule editing.
type RecordingSearch struct {
	Items              []SelectionItem
	Page, Pages, Total int
}

// SearchRecordings never resolves a source or includes a foreign playlist owner.
func (s *Store) SearchRecordings(ctx context.Context, ch config.Channel, query string, page int) (RecordingSearch, error) {
	rules, err := json.Marshal(ch.VideoOverrides)
	if err != nil {
		return RecordingSearch{}, err
	}

	cte := `WITH recordings AS (SELECT v.id,v.title,v.published,CASE WHEN v.state='replay-ready' THEN 'replay' ELSE COALESCE(k.kind,'') END AS kind,COALESCE(r.value,'') AS override FROM videos v LEFT JOIN video_content k ON k.video_id=v.id LEFT JOIN json_each(?) r ON r.key=v.id WHERE v.channel_id=? AND v.state IN ('recorded','replay-ready') AND instr(v.search_title||' '||lower(v.id),?)>0) `
	args := []any{string(rules), ch.ID, strings.ToLower(strings.TrimSpace(query))}
	result := RecordingSearch{Items: []SelectionItem{}}

	tx, err := s.projectionReader().BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return result, err
	}
	defer rollback(tx)

	if err = tx.QueryRowContext(ctx, cte+`SELECT count(*) FROM recordings`, args...).Scan(&result.Total); err != nil {
		return result, err
	}

	result.Pages = max(1, (result.Total+9)/10)
	result.Page = max(1, min(page, result.Pages))

	rows, err := tx.QueryContext(ctx, cte+`SELECT id,title,kind,override FROM recordings ORDER BY published DESC,id LIMIT 10 OFFSET ?`, append(args, (result.Page-1)*10)...)
	if err != nil {
		return result, err
	}
	defer closeRows(rows)

	for rows.Next() {
		var item SelectionItem
		if err = rows.Scan(&item.ID, &item.Title, &item.Kind, &item.Override); err != nil {
			return result, err
		}

		result.Items = append(result.Items, item)
	}

	return result, rows.Err()
}

// RecordingKinds reads source evidence for one bounded administration page.
func (s *Store) RecordingKinds(ctx context.Context, ids []string) (map[string]string, error) {
	if len(ids) > 100 {
		ids = ids[:100]
	}

	encoded, err := json.Marshal(ids)
	if err != nil {
		return nil, err
	}

	rows, err := s.projectionReader().QueryContext(ctx, `SELECT v.id,CASE WHEN v.state='replay-ready' THEN 'replay' ELSE COALESCE(k.kind,'') END FROM json_each(?) i JOIN videos v ON v.id=i.value LEFT JOIN video_content k ON k.video_id=v.id`, string(encoded))
	if err != nil {
		return nil, err
	}
	defer closeRows(rows)

	kinds := map[string]string{}

	for rows.Next() {
		var id, kind string
		if err = rows.Scan(&id, &kind); err != nil {
			return nil, err
		}

		kinds[id] = kind
	}

	return kinds, rows.Err()
}
