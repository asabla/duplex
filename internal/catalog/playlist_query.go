package catalog

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// ChannelPolicy supplies current YAML policy authority to playlist read queries.
type (
	ChannelPolicy struct {
		ID, Mode  string
		Limit     int
		Enabled   bool
		Overrides []PlaylistOverride
	}
	// PlaylistOverride supplies one collection policy override.
	PlaylistOverride struct {
		ID, Mode string
		Limit    int
	}
	// PlaylistFilter bounds and filters one playlist page.
	PlaylistFilter struct {
		Policies                                          []ChannelPolicy
		Query, Channel, Mode, Failure, Sort, Availability string
		Page, PerPage                                     int
	}
	// PlaylistRow combines catalogue details with effective YAML policy.
	PlaylistRow struct {
		Playlist
		Mode      string `json:"mode"`
		Limit     int    `json:"limit"`
		Inherited bool   `json:"inherited"`
		Selected  int    `json:"selected"`
	}
	// PlaylistPage contains one bounded result page and its total count.
	PlaylistPage struct {
		Items   []PlaylistRow `json:"items"`
		Page    int           `json:"page"`
		PerPage int           `json:"per_page"`
		Total   int           `json:"total"`
	}
)

func playlistPolicies(policies []ChannelPolicy) (string, []any) {
	channelRows, overrideRows := []string{}, []string{}
	channelArgs, overrideArgs := []any{}, []any{}

	for _, p := range policies {
		channelRows = append(channelRows, "(?,?,?,?)")

		channelArgs = append(channelArgs, p.ID, p.Mode, p.Limit, p.Enabled)
		for _, override := range p.Overrides {
			overrideRows = append(overrideRows, "(?,?,?)")
			overrideArgs = append(overrideArgs, override.ID, override.Mode, override.Limit)
		}
	}

	channels := `policies(id,mode,amount,enabled) AS (SELECT '', '',0,0 WHERE 0)`
	overrides := `overrides(id,mode,amount) AS (SELECT '', '',0 WHERE 0)`

	if len(channelRows) > 0 {
		channels = `policies(id,mode,amount,enabled) AS (VALUES ` + strings.Join(channelRows, ",") + `)`
	}

	if len(overrideRows) > 0 {
		overrides = `overrides(id,mode,amount) AS (VALUES ` + strings.Join(overrideRows, ",") + `)`
	}

	cte := `WITH ` + channels + `,` + overrides + `,effective AS (SELECT p.*,CASE WHEN c.enabled=0 THEN 'skip' WHEN o.mode IS NULL OR o.mode='inherit' THEN c.mode ELSE o.mode END AS policy,CASE WHEN o.mode='latest' THEN o.amount ELSE c.amount END AS amount,(o.mode IS NULL OR o.mode='inherit') AS inherited FROM playlists p JOIN policies c ON c.id=p.channel_id LEFT JOIN overrides o ON o.id=p.id) `

	return cte, append(channelArgs, overrideArgs...)
}

// PlaylistsPage filters and counts in SQLite before loading the requested rows.
func (s *Store) PlaylistsPage(ctx context.Context, f PlaylistFilter) (PlaylistPage, error) {
	result := PlaylistPage{Items: []PlaylistRow{}, PerPage: f.PerPage}
	if f.Availability != "" && f.Availability != "available" && f.Availability != "missing" {
		return result, errors.New("invalid playlist availability")
	}

	if result.PerPage != 25 && result.PerPage != 50 && result.PerPage != 100 {
		result.PerPage = 25
	}

	cte, args := playlistPolicies(f.Policies)
	where, filterArgs := playlistWhere(f)
	args = append(args, filterArgs...)

	order, err := playlistOrder(f.Sort)
	if err != nil {
		return result, err
	}

	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return result, err
	}
	defer rollback(tx)

	if err = tx.QueryRowContext(ctx, cte+`SELECT count(*) FROM effective p WHERE `+where, args...).Scan(&result.Total); err != nil {
		return result, err
	}

	result.Page = max(1, min(max(1, f.Page), max(1, (result.Total+result.PerPage-1)/result.PerPage)))
	args = append(args, result.PerPage, (result.Page-1)*result.PerPage)

	rows, err := tx.QueryContext(ctx, cte+`SELECT p.id,p.channel_id,p.title,p.description,p.thumbnail,p.season,p.synced_at,p.error,p.available,p.policy,p.amount,p.inherited,(SELECT count(*) FROM playlist_members m WHERE m.playlist_id=p.id AND m.selected=1) FROM effective p WHERE `+where+` ORDER BY `+order+` LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return result, err
	}

	result.Items, err = readPlaylists(rows)
	if err != nil {
		return result, err
	}

	return result, tx.Commit()
}

func playlistWhere(f PlaylistFilter) (string, []any) {
	var args []any

	clauses := []string{"1=1"}
	if f.Query != "" {
		clauses = append(clauses, `instr(lower(p.title||' '||p.id),?)>0`)
		args = append(args, strings.ToLower(f.Query))
	}

	if f.Channel != "" {
		clauses = append(clauses, `p.channel_id=?`)
		args = append(args, f.Channel)
	}

	if f.Mode != "" {
		clauses = append(clauses, `p.policy=?`)
		args = append(args, f.Mode)
	}

	if f.Availability != "" {
		clauses = append(clauses, `p.available=?`)
		args = append(args, f.Availability == "available")
	}

	if f.Failure == "failed" {
		clauses = append(clauses, `p.error<>''`)
	}

	return strings.Join(clauses, " AND "), args
}

func playlistOrder(value string) (string, error) {
	switch value {
	case "", "title":
		return "p.title COLLATE NOCASE,p.id", nil
	case "title_desc":
		return "p.title COLLATE NOCASE DESC,p.id", nil
	case "season":
		return "p.channel_id,p.season,p.id", nil
	case "season_desc":
		return "p.channel_id,p.season DESC,p.id", nil
	case "updated":
		return "p.synced_at DESC,p.id", nil
	case "updated_oldest":
		return "p.synced_at,p.id", nil
	default:
		return "", errors.New("invalid playlist sort")
	}
}

func readPlaylists(rows *sql.Rows) ([]PlaylistRow, error) {
	defer closeRows(rows)

	result := []PlaylistRow{}

	for rows.Next() {
		var p PlaylistRow
		if err := rows.Scan(&p.ID, &p.ChannelID, &p.Title, &p.Description, &p.Thumbnail, &p.Season, &p.SyncedAt, &p.Error, &p.Available, &p.Mode, &p.Limit, &p.Inherited, &p.Selected); err != nil {
			return nil, err
		}

		result = append(result, p)
	}

	return result, rows.Err()
}
