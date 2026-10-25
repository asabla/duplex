package catalog

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// Visibility supplies configuration authority to read queries without copying
// the catalogue into the application. Playlist exclusions take effect at once.
type Visibility struct {
	Channels         []string
	PlaylistChannels []string
	SkippedPlaylists []string
}

// VideoFilter describes one bounded catalogue page.
type VideoFilter struct {
	Visibility
	Query, Channel, Playlist, Lifecycle, Preparation, Inclusion, Sort string
	Page, PerPage                                                     int
}

// VideoPage contains only the requested rows and consistent result counts.
type VideoPage struct {
	Items   []Video `json:"items"`
	Page    int     `json:"page"`
	PerPage int     `json:"per_page"`
	Total   int     `json:"total"`
}

// Counts is a small service summary, not a hydrated catalogue.
type Counts struct {
	Discovered int `json:"discovered"`
	Selected   int `json:"selected"`
	Ready      int `json:"ready"`
	Failed     int `json:"failed"`
}

func valueSet(name string, values []string, args *[]any) string {
	if len(values) == 0 {
		return name + `(id) AS (SELECT '' WHERE 0)`
	}
	marks := make([]string, len(values))
	for i, v := range values {
		marks[i] = "(?)"
		*args = append(*args, v)
	}
	return name + `(id) AS (VALUES ` + strings.Join(marks, ",") + `)`
}

func selectedCTE(v Visibility) (string, []any) {
	args := []any{}
	parts := []string{valueSet("active_channels", v.Channels, &args), valueSet("playlist_channels", v.PlaylistChannels, &args), valueSet("skipped_playlists", v.SkippedPlaylists, &args)}
	parts = append(parts, `selected(id) AS (
SELECT v.id FROM videos v JOIN active_channels c ON c.id=v.channel_id WHERE v.selected=1
UNION SELECT m.video_id FROM playlist_members m JOIN playlists p ON p.id=m.playlist_id JOIN playlist_channels c ON c.id=p.channel_id WHERE m.selected=1 AND NOT EXISTS(SELECT 1 FROM skipped_playlists x WHERE x.id=p.id))`)
	return "WITH " + strings.Join(parts, ",") + " ", args
}

func videoWhere(f VideoFilter) (string, []any, error) {
	clauses := []string{"1=1"}
	args := []any{}
	if f.Query != "" {
		clauses = append(clauses, `instr(v.search_title||' '||lower(v.id||' '||v.channel_id),?)>0`)
		args = append(args, strings.ToLower(f.Query))
	}
	if f.Channel != "" {
		clauses = append(clauses, `(v.channel_id=? OR EXISTS(SELECT 1 FROM playlist_members m JOIN playlists p ON p.id=m.playlist_id WHERE m.video_id=v.id AND p.channel_id=?))`)
		args = append(args, f.Channel, f.Channel)
	}
	if f.Playlist != "" {
		clauses = append(clauses, `EXISTS(SELECT 1 FROM playlist_members m WHERE m.video_id=v.id AND m.playlist_id=?)`)
		args = append(args, f.Playlist)
	}
	if f.Lifecycle != "" {
		clauses = append(clauses, `v.state=?`)
		args = append(args, f.Lifecycle)
	}
	if f.Preparation != "" {
		clauses = append(clauses, `v.preparation=?`)
		args = append(args, f.Preparation)
	}
	switch f.Inclusion {
	case "selected":
		clauses = append(clauses, `EXISTS(SELECT 1 FROM selected x WHERE x.id=v.id)`)
	case "outside":
		clauses = append(clauses, `NOT EXISTS(SELECT 1 FROM selected x WHERE x.id=v.id)`)
	case "all", "":
	default:
		return "", nil, errors.New("invalid library inclusion filter")
	}
	return strings.Join(clauses, " AND "), args, nil
}

func videoOrder(sort string) (string, error) {
	switch sort {
	case "", "newest":
		return "v.published DESC,v.id", nil
	case "oldest":
		return "v.published,v.id", nil
	case "title":
		return "v.title COLLATE NOCASE,v.id", nil
	case "updated":
		return "v.updated DESC,v.id", nil
	default:
		return "", errors.New("invalid video sort")
	}
}

// VideosPage queries a single page and its count from the same read snapshot.
func (s *Store) VideosPage(ctx context.Context, f VideoFilter) (VideoPage, error) {
	result := VideoPage{PerPage: f.PerPage}
	if result.PerPage != 25 && result.PerPage != 50 && result.PerPage != 100 {
		result.PerPage = 25
	}
	cte, args := selectedCTE(f.Visibility)
	where, filters, err := videoWhere(f)
	if err != nil {
		return result, err
	}
	args = append(args, filters...)
	order, err := videoOrder(f.Sort)
	if err != nil {
		return result, err
	}
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return result, err
	}
	defer rollback(tx)
	if err = tx.QueryRowContext(ctx, cte+`SELECT count(*) FROM videos v WHERE `+where, args...).Scan(&result.Total); err != nil {
		return result, err
	}
	result.Page = max(1, min(max(1, f.Page), max(1, (result.Total+result.PerPage-1)/result.PerPage)))
	columns := `v.id,v.channel_id,v.title,v.description,v.state,v.published,v.year,v.number,EXISTS(SELECT 1 FROM selected x WHERE x.id=v.id),v.revision,v.height,v.fallback,v.size,v.updated,v.preparation,v.preparation_error,v.metadata_at`
	args = append(args, result.PerPage, (result.Page-1)*result.PerPage)
	rows, err := tx.QueryContext(ctx, cte+`SELECT `+columns+` FROM videos v WHERE `+where+` ORDER BY `+order+` LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return result, err
	}
	result.Items, err = readVideos(rows)
	if err != nil {
		return result, err
	}
	return result, tx.Commit()
}

func readVideos(rows *sql.Rows) ([]Video, error) {
	defer closeRows(rows)
	result := []Video{}
	for rows.Next() {
		var v Video
		if err := rows.Scan(&v.ID, &v.ChannelID, &v.Title, &v.Description, &v.State, &v.Published, &v.Year, &v.Number, &v.Exposed, &v.Revision, &v.Height, &v.Fallback, &v.Size, &v.Updated, &v.Preparation, &v.PreparationError, &v.MetadataAt); err != nil {
			return nil, err
		}
		result = append(result, v)
	}
	return result, rows.Err()
}

// Summary counts visible resources without selecting video descriptions.
func (s *Store) Summary(ctx context.Context, v Visibility) (Counts, error) {
	cte, args := selectedCTE(v)
	var c Counts
	err := s.DB.QueryRowContext(ctx, cte+`SELECT count(*),COALESCE(sum(EXISTS(SELECT 1 FROM selected x WHERE x.id=v.id)),0),COALESCE(sum(v.preparation='ready' AND v.state IN ('recorded','replay-ready') AND EXISTS(SELECT 1 FROM selected x WHERE x.id=v.id)),0),COALESCE(sum(v.preparation='failed' AND EXISTS(SELECT 1 FROM selected x WHERE x.id=v.id)),0) FROM videos v`, args...).Scan(&c.Discovered, &c.Selected, &c.Ready, &c.Failed)
	return c, err
}
