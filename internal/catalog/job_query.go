package catalog

import (
	"context"
	"database/sql"
	"errors"
)

type (
	JobFilter struct {
		Channel, State, Sort string
		Page, PerPage        int
	}
	JobPage struct {
		Items                []Job `json:"items"`
		Page, PerPage, Total int
	}
)

// FilterJobs reads one filtered history page. The legacy Jobs response stays intact.
func (s *Store) FilterJobs(ctx context.Context, f JobFilter) (JobPage, error) {
	result := JobPage{Items: []Job{}, PerPage: f.PerPage}
	if result.PerPage != 25 && result.PerPage != 50 && result.PerPage != 100 {
		result.PerPage = 25
	}
	args := []any{}
	where := "1=1"
	if f.Channel != "" {
		where += ` AND channel_id=?`
		args = append(args, f.Channel)
	}
	if f.State != "" {
		where += ` AND state=?`
		args = append(args, f.State)
	}
	order := "id DESC"
	switch f.Sort {
	case "", "newest":
	case "oldest":
		order = "id"
	default:
		return result, errors.New("invalid job sort")
	}
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return result, err
	}
	defer rollback(tx)
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM sync_jobs WHERE `+where, args...).Scan(&result.Total); err != nil {
		return result, err
	}
	result.Page = max(1, min(max(1, f.Page), max(1, (result.Total+result.PerPage-1)/result.PerPage)))
	args = append(args, result.PerPage, (result.Page-1)*result.PerPage)
	rows, err := tx.QueryContext(ctx, `SELECT id,channel_id,state,started,finished,error,stage,discovered,prepared FROM sync_jobs WHERE `+where+` ORDER BY `+order+` LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return result, err
	}
	result.Items, err = readJobs(rows)
	if err != nil {
		return result, err
	}
	return result, tx.Commit()
}
