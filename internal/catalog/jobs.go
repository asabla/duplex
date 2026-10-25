package catalog

import (
	"context"
	"database/sql"
	"time"
)

// Job records discovery progress separately from durable media preparation tasks.
type Job struct {
	ID         int64  `json:"id"`
	ChannelID  string `json:"channel_id"`
	State      string `json:"state"`
	Started    int64  `json:"started"`
	Finished   int64  `json:"finished"`
	Error      string `json:"error,omitempty"`
	Stage      string `json:"stage,omitempty"`
	Discovered int    `json:"discovered"`
	Prepared   int    `json:"prepared"`
}

// StartJob creates a durable running discovery job.
func (s *Store) StartJob(ctx context.Context, id string) (int64, error) {
	r, e := s.DB.ExecContext(ctx, `INSERT INTO sync_jobs(channel_id,state,started) VALUES(?,'running',?)`, id, time.Now().Unix())
	if e != nil {
		return 0, e
	}

	return r.LastInsertId()
}

// FinishJob records a redacted result and retains the previous selection on failure.
func (s *Store) FinishJob(ctx context.Context, id int64, ch string, err error) error {
	state, msg := "complete", ""
	if err != nil {
		state = "failed"
		msg = "source or representation refresh failed; previous complete catalogue retained"
	}

	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer rollback(tx)

	if _, e = tx.ExecContext(ctx, `UPDATE sync_jobs SET state=?,finished=?,error=? WHERE id=?`, state, time.Now().Unix(), msg, id); e != nil {
		return e
	}

	if err != nil {
		if _, e = tx.ExecContext(ctx, `UPDATE channels SET error=? WHERE id=?`, msg, ch); e != nil {
			return e
		}
	}

	// Durable selections and preparation jobs are the restart checkpoint. Flat
	// enumeration is repeated after interruption, so completed staging is unused.
	if _, e = tx.ExecContext(ctx, `DELETE FROM discovery_items WHERE run_id=?`, id); e != nil {
		return e
	}

	return tx.Commit()
}

// Jobs returns the latest 100 jobs for API compatibility; FilterJobs reads bounded history.
func (s *Store) Jobs(ctx context.Context) ([]Job, error) {
	rows, e := s.DB.QueryContext(ctx, `SELECT id,channel_id,state,started,finished,error,stage,discovered,prepared FROM sync_jobs ORDER BY id DESC LIMIT 100`)
	if e != nil {
		return nil, e
	}

	return readJobs(rows)
}

func readJobs(rows *sql.Rows) ([]Job, error) {
	defer closeRows(rows)

	out := []Job{}

	for rows.Next() {
		var j Job
		if e := rows.Scan(&j.ID, &j.ChannelID, &j.State, &j.Started, &j.Finished, &j.Error, &j.Stage, &j.Discovered, &j.Prepared); e != nil {
			return nil, e
		}

		out = append(out, j)
	}

	return out, rows.Err()
}

// JobsPage reads a bounded page and its count from the same SQLite snapshot.
// Jobs remains the latest-100 compatibility view for existing API consumers.
func (s *Store) JobsPage(ctx context.Context, number, size int) ([]Job, int, error) {
	size = max(1, min(size, 100))

	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, 0, err
	}
	defer rollback(tx)

	var total int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM sync_jobs`).Scan(&total); err != nil {
		return nil, 0, err
	}

	number = max(1, min(number, max(1, (total+size-1)/size)))

	rows, err := tx.QueryContext(ctx, `SELECT id,channel_id,state,started,finished,error,stage,discovered,prepared FROM sync_jobs ORDER BY id DESC LIMIT ? OFFSET ?`, size, (number-1)*size)
	if err != nil {
		return nil, 0, err
	}

	jobs, err := readJobs(rows)
	if err != nil {
		return nil, 0, err
	}

	return jobs, total, tx.Commit()
}
