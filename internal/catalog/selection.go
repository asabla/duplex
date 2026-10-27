package catalog

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"duplex/internal/config"
)

// SelectionPreview describes last-discovered recordings, never a source fetch.
type SelectionPreview struct {
	Known       int             `json:"known"`
	UnknownKind int             `json:"unknown_kind"`
	Selected    int             `json:"selected"`
	Added       int             `json:"added"`
	Removed     int             `json:"removed"`
	Items       []SelectionItem `json:"items"`
}

// SelectionItem is a bounded preview row, without media locators.
type SelectionItem struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Kind     string `json:"kind"`
	Override string `json:"override,omitempty"`
}

func uploadSelection(ch config.Channel, run int64) (string, []any, error) {
	if err := ch.ValidateSelection(); err != nil {
		return "", nil, err
	}

	if ch.Content == nil && len(ch.VideoOverrides) == 0 {
		query, args := sharedUploadSelection(ch, run)
		return query, args, nil
	}

	policies := [][]any{{"*", ch.Selection().Mode, ch.Selection().Limit}}
	if ch.Content != nil {
		policies = nil

		for _, kind := range []string{"upload", "short", "replay"} {
			p := ch.ContentSelection(kind)
			policies = append(policies, []any{kind, p.Mode, p.Limit})
		}
	}

	encoded, err := json.Marshal(policies)
	if err != nil {
		return "", nil, err
	}

	overrides, err := json.Marshal(ch.VideoOverrides)
	if err != nil {
		return "", nil, err
	}

	cte := `WITH policies(kind,mode,n) AS (SELECT json_extract(value,'$[0]'),json_extract(value,'$[1]'),json_extract(value,'$[2]') FROM json_each(?)), rules(id,mode) AS (SELECT key,value FROM json_each(?)),
known AS (SELECT v.id,v.title,v.published,v.selected,CASE WHEN v.state='replay-ready' THEN 'replay' ELSE COALESCE(k.kind,'') END AS kind,COALESCE(r.mode,'') AS override FROM videos v LEFT JOIN video_content k ON k.video_id=v.id LEFT JOIN rules r ON r.id=v.id WHERE v.channel_id=? AND v.state IN ('recorded','replay-ready')),
eligible AS (SELECT * FROM known WHERE override<>'exclude'`
	args := []any{string(encoded), string(overrides), ch.ID}

	if run > 0 {
		cte += ` AND id IN (SELECT video_id FROM discovery_items WHERE run_id=? AND collection='uploads')`

		args = append(args, run)
	}

	cte += `), ranked AS (SELECT *,ROW_NUMBER() OVER(PARTITION BY `
	if ch.Content == nil {
		cte += `'*'`
	} else {
		cte += `kind`
	}

	cte += ` ORDER BY published DESC,id) AS ordinal FROM eligible), desired AS (SELECT r.* FROM ranked r LEFT JOIN policies p ON p.kind=`
	if ch.Content == nil {
		cte += `'*'`
	} else {
		cte += `r.kind`
	}

	return cte + ` WHERE r.override='keep' OR p.mode='all' OR (p.mode='latest' AND r.ordinal<=p.n)) `, args, nil
}

// Shared windows need no per-kind ranking or override join. Retain that simple
// query for existing configurations while using the same durable checkpoint.
func sharedUploadSelection(ch config.Channel, run int64) (string, []any) {
	query := `WITH known AS (SELECT v.id,v.title,v.published,v.selected,CASE WHEN v.state='replay-ready' THEN 'replay' ELSE COALESCE(k.kind,'') END AS kind,'' AS override FROM videos v LEFT JOIN video_content k ON k.video_id=v.id WHERE v.channel_id=? AND v.state IN ('recorded','replay-ready')), desired AS (SELECT * FROM known WHERE true`
	args := []any{ch.ID}

	if run > 0 {
		query += ` AND id IN (SELECT video_id FROM discovery_items WHERE run_id=? AND collection='uploads')`

		args = append(args, run)
	}

	if policy := ch.Selection(); policy.Mode == "latest" {
		query += ` ORDER BY published DESC,id LIMIT ?`

		args = append(args, policy.Limit)
	}

	return query + `) `, args
}

// PreviewUploads reads consistent counts and at most 25 selected recordings.
func (s *Store) PreviewUploads(ctx context.Context, ch config.Channel) (SelectionPreview, error) {
	cte, args, err := uploadSelection(ch, 0)
	if err != nil {
		return SelectionPreview{}, err
	}

	tx, err := s.projectionReader().BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return SelectionPreview{}, err
	}
	defer rollback(tx)

	p := SelectionPreview{Items: []SelectionItem{}}

	err = tx.QueryRowContext(ctx, cte+`SELECT (SELECT count(*) FROM known),(SELECT count(*) FROM known WHERE kind=''),(SELECT count(*) FROM desired),(SELECT count(*) FROM desired WHERE selected=0),(SELECT count(*) FROM videos WHERE channel_id=? AND selected=1 AND id NOT IN (SELECT id FROM desired))`, append(args, ch.ID)...).Scan(&p.Known, &p.UnknownKind, &p.Selected, &p.Added, &p.Removed)
	if err != nil {
		return p, err
	}

	rows, err := tx.QueryContext(ctx, cte+`SELECT id,title,kind,override FROM desired ORDER BY published DESC,id LIMIT 25`, args...)
	if err != nil {
		return p, err
	}
	defer closeRows(rows)

	for rows.Next() {
		var item SelectionItem
		if err = rows.Scan(&item.ID, &item.Title, &item.Kind, &item.Override); err != nil {
			return p, err
		}

		p.Items = append(p.Items, item)
	}

	return p, rows.Err()
}

// ReconcileUploadRules publishes only a successfully enumerated window.
func (s *Store) ReconcileUploadRules(ctx context.Context, run int64, ch config.Channel) error {
	cte, args, err := uploadSelection(ch, run)
	if err != nil {
		return err
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)

	digest, err := recordingRulesDigest(ch)
	if err != nil {
		return err
	}

	if run == 0 {
		unchanged, failure := unchangedRecordingRules(ctx, tx, ch, digest)
		if failure != nil || unchanged {
			return failure
		}
	}

	_, err = tx.ExecContext(ctx, cte+`UPDATE videos SET selected=0,exposed=0 WHERE channel_id=? AND (selected<>0 OR exposed<>0) AND id NOT IN (SELECT id FROM desired)`, append(args, ch.ID)...)
	if err != nil {
		return err
	}

	_, err = tx.ExecContext(ctx, cte+`UPDATE videos SET selected=1,exposed=(revision<>'' AND number>0) WHERE id IN (SELECT id FROM desired) AND (selected<>1 OR exposed<>(revision<>'' AND number>0))`, args...)
	if err != nil {
		return err
	}

	if run > 0 {
		if _, err = tx.ExecContext(ctx, `UPDATE channels SET synced_at=?,error='' WHERE id=?`, time.Now().Unix(), ch.ID); err != nil {
			return err
		}
	}

	if _, err = tx.ExecContext(ctx, `INSERT INTO upload_rule_state(channel_id,digest) SELECT id,? FROM channels WHERE id=? ON CONFLICT(channel_id) DO UPDATE SET digest=excluded.digest`, digest, ch.ID); err != nil {
		return err
	}

	return tx.Commit()
}

func unchangedRecordingRules(ctx context.Context, tx *sql.Tx, ch config.Channel, digest string) (bool, error) {
	var previous string

	err := tx.QueryRowContext(ctx, `SELECT digest FROM upload_rule_state WHERE channel_id=?`, ch.ID).Scan(&previous)
	if errors.Is(err, sql.ErrNoRows) {
		return ch.Content == nil && len(ch.VideoOverrides) == 0, nil
	}

	return previous == digest, err
}

func recordingRulesDigest(ch config.Channel) (string, error) {
	overrides := ch.VideoOverrides
	if len(overrides) == 0 {
		overrides = nil
	}

	data, err := json.Marshal(struct {
		Selection config.Selection
		Content   *config.ContentPolicy
		Overrides map[string]string
	}{ch.Selection(), ch.Content, overrides})
	if err != nil {
		return "", err
	}

	digest := sha256.Sum256(data)

	return hex.EncodeToString(digest[:]), nil
}
