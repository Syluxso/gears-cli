package verbs

import (
	"database/sql"
	"encoding/json"
	"os"

	"github.com/Syluxso/gears-cli/internal/store"
)

// ---------------------------------------------------------------- export

// Tables is every table in the local record, in a stable order. The export
// is the record as the API will receive it on first sync: rows as they are,
// ids and timestamps already client-minted.
var Tables = []string{
	"session_entries", "artifacts", "artifact_tags",
	"desk_items", "card_states", "card_sequence", "cards",
	"runs", "run_steps",
}

type ExportIn struct {
	Out string `json:"out,omitempty" jsonschema:"File to write. Empty returns the record inline."`
}

type ExportOut struct {
	Project    string                      `json:"project"`
	ProjectID  string                      `json:"projectId"`
	Prefix     string                      `json:"prefix"`
	ExportedAt string                      `json:"exportedAt"`
	Counts     map[string]int              `json:"counts"`
	Tables     map[string][]map[string]any `json:"tables,omitempty"`
	File       string                      `json:"file,omitempty"`
}

// Export dumps the local record to JSON: every table, every row, including
// closed and soft-deleted ones. With Out set it writes the file and returns
// only the counts.
func Export(c Ctx, in ExportIn) (*ExportOut, error) {
	out := &ExportOut{Project: c.Project.Slug, ProjectID: c.Project.ID, Prefix: c.Project.Prefix,
		ExportedAt: store.Now(), Counts: map[string]int{}, Tables: map[string][]map[string]any{}}
	if store.Exists(c.Project) {
		db, err := store.Open(c.Project)
		if err != nil {
			return nil, err
		}
		defer db.Close()
		for _, t := range Tables {
			rows, err := dump(db, t)
			if err != nil {
				return nil, err
			}
			out.Tables[t] = rows
			out.Counts[t] = len(rows)
		}
	} else {
		for _, t := range Tables {
			out.Tables[t] = []map[string]any{}
			out.Counts[t] = 0
		}
	}
	if in.Out != "" {
		b, err := json.MarshalIndent(out, "", "  ")
		if err != nil {
			return nil, err
		}
		if err := os.WriteFile(in.Out, append(b, '\n'), 0o644); err != nil {
			return nil, err
		}
		out.File = in.Out
		out.Tables = nil
	}
	return out, nil
}

func dump(db *sql.DB, table string) ([]map[string]any, error) {
	rows, err := db.Query(`SELECT * FROM ` + table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		m := map[string]any{}
		for i, col := range cols {
			switch v := vals[i].(type) {
			case []byte:
				m[col] = string(v)
			default:
				m[col] = v
			}
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
