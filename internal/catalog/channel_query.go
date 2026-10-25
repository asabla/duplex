package catalog

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

type (
	// ChannelFilter restricts one channel page to identities permitted by YAML.
	ChannelFilter struct {
		IDs                  []string
		Query, Failure, Sort string
		Page, PerPage        int
	}
	// ChannelPage contains a bounded set of channels and the matching count.
	ChannelPage struct {
		Items   []Channel `json:"items"`
		Page    int       `json:"page"`
		PerPage int       `json:"per_page"`
		Total   int       `json:"total"`
	}
)

// ChannelsPage reads only followed channel rows selected by the current settings.
func (s *Store) ChannelsPage(ctx context.Context, f ChannelFilter) (ChannelPage, error) {
	result := ChannelPage{Items: []Channel{}, PerPage: f.PerPage}
	if result.PerPage != 25 && result.PerPage != 50 && result.PerPage != 100 {
		result.PerPage = 25
	}
	args := []any{}
	cte := "WITH " + valueSet("followed", f.IDs, &args) + " "
	where := `EXISTS(SELECT 1 FROM followed f WHERE f.id=c.id)`
	if f.Query != "" {
		where += ` AND instr(lower(c.title||' '||c.id),?)>0`
		args = append(args, strings.ToLower(f.Query))
	}
	if f.Failure == "failed" {
		where += ` AND c.error<>''`
	}
	order := "c.title COLLATE NOCASE,c.id"
	switch f.Sort {
	case "", "title":
	case "updated":
		order = "c.synced_at DESC,c.id"
	default:
		return result, errors.New("invalid channel sort")
	}
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return result, err
	}
	defer rollback(tx)
	if err = tx.QueryRowContext(ctx, cte+`SELECT count(*) FROM channels c WHERE `+where, args...).Scan(&result.Total); err != nil {
		return result, err
	}
	result.Page = max(1, min(max(1, f.Page), max(1, (result.Total+result.PerPage-1)/result.PerPage)))
	args = append(args, result.PerPage, (result.Page-1)*result.PerPage)
	rows, err := tx.QueryContext(ctx, cte+`SELECT c.id,c.title,c.synced_at,c.error FROM channels c WHERE `+where+` ORDER BY `+order+` LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return result, err
	}
	result.Items, err = readChannels(rows)
	if err != nil {
		return result, err
	}
	return result, tx.Commit()
}

func readChannels(rows *sql.Rows) ([]Channel, error) {
	defer closeRows(rows)
	out := []Channel{}
	for rows.Next() {
		var c Channel
		if err := rows.Scan(&c.ID, &c.Title, &c.SyncedAt, &c.Error); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
