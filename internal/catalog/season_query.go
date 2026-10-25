package catalog

import (
	"context"
	"database/sql"
	"strings"
)

// SeasonFilter selects a bounded artwork picker page from durable identities.
type SeasonFilter struct {
	Channel, Query, Scope string
	Page, PerPage         int
}

// ArtworkSeason retains a meaningful title independently of its artwork scope.
type ArtworkSeason struct {
	Scope  string `json:"scope"`
	Title  string `json:"title"`
	Kind   string `json:"kind"`
	Number int    `json:"number"`
}

// SeasonPage includes unavailable seasons so their overrides remain editable.
type SeasonPage struct {
	Items   []ArtworkSeason `json:"items"`
	Total   int             `json:"total"`
	Page    int             `json:"page"`
	PerPage int             `json:"per_page"`
}

// ArtworkSeasons filters and counts in SQLite before hydrating one picker page.
func (s *Store) ArtworkSeasons(ctx context.Context, f SeasonFilter) (SeasonPage, error) {
	result := SeasonPage{Items: []ArtworkSeason{}, PerPage: f.PerPage}
	if result.PerPage != 25 && result.PerPage != 50 && result.PerPage != 100 {
		result.PerPage = 25
	}
	cte := `WITH seasons AS (SELECT 'year:'||year AS scope,'Year '||year AS title,'year' AS kind,year AS number FROM videos WHERE channel_id=? AND year BETWEEN 1000 AND 9999 GROUP BY year UNION ALL SELECT 'playlist:'||id,COALESCE(NULLIF(title,''),id),'playlist',season FROM playlists WHERE channel_id=?) `
	where, args := seasonWhere(f)
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return result, err
	}
	defer rollback(tx)
	if err = tx.QueryRowContext(ctx, cte+`SELECT count(*) FROM seasons WHERE `+where, args...).Scan(&result.Total); err != nil {
		return result, err
	}
	result.Page = max(1, min(max(1, f.Page), max(1, (result.Total+result.PerPage-1)/result.PerPage)))
	args = append(args, result.PerPage, (result.Page-1)*result.PerPage)
	rows, err := tx.QueryContext(ctx, cte+`SELECT scope,title,kind,number FROM seasons WHERE `+where+` ORDER BY number,scope LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return result, err
	}
	result.Items, err = readArtworkSeasons(rows)
	if err != nil {
		return result, err
	}
	return result, tx.Commit()
}

func seasonWhere(f SeasonFilter) (string, []any) {
	clauses := []string{"1=1"}
	args := []any{f.Channel, f.Channel}
	if f.Query != "" {
		clauses = append(clauses, `instr(lower(title||' '||scope),?)>0`)
		args = append(args, strings.ToLower(f.Query))
	}
	if f.Scope != "" {
		clauses = append(clauses, `scope=?`)
		args = append(args, f.Scope)
	}
	return strings.Join(clauses, " AND "), args
}

func readArtworkSeasons(rows *sql.Rows) ([]ArtworkSeason, error) {
	defer closeRows(rows)
	out := []ArtworkSeason{}
	for rows.Next() {
		var season ArtworkSeason
		if err := rows.Scan(&season.Scope, &season.Title, &season.Kind, &season.Number); err != nil {
			return nil, err
		}
		out = append(out, season)
	}
	return out, rows.Err()
}
