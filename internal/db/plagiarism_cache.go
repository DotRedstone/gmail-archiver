package db

import (
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

var ErrPlagiarismIncomplete = errors.New("db: plagiarism index incomplete")

// GetMaterializedPlagiarismReport reads the last report when the indexed
// submissions have not changed. A cache miss recomputes it from the database.
func (d *DB) GetMaterializedPlagiarismReport(assignmentID string) ([]*PlagiarismPair, error) {
	progress, err := d.AssignmentPlagiarismProgress(assignmentID)
	if err != nil {
		return nil, err
	}
	if progress.Processing > 0 || progress.Failed > 0 {
		return nil, ErrPlagiarismIncomplete
	}
	lastSubmission, lastFile, err := d.plagiarismSourceVersion(assignmentID)
	if err != nil {
		return nil, err
	}
	var cachedSubmission, cachedFile int64
	var raw string
	err = d.conn.QueryRow(`SELECT last_submission_id, last_file_id, report_json
		FROM plagiarism_report_cache WHERE assignment_id=?`, assignmentID).
		Scan(&cachedSubmission, &cachedFile, &raw)
	if err == nil && cachedSubmission == lastSubmission && cachedFile == lastFile {
		var report []*PlagiarismPair
		if json.Unmarshal([]byte(raw), &report) == nil {
			return report, nil
		}
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	report, err := d.GetAssignmentPlagiarismReport(assignmentID)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		return nil, err
	}
	_, err = d.conn.Exec(`INSERT INTO plagiarism_report_cache
		(assignment_id, last_submission_id, last_file_id, report_json, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(assignment_id) DO UPDATE SET
		last_submission_id=excluded.last_submission_id,
		last_file_id=excluded.last_file_id,
		report_json=excluded.report_json,
		updated_at=excluded.updated_at`,
		assignmentID, lastSubmission, lastFile, string(encoded), time.Now().UTC().Format(time.RFC3339Nano))
	return report, err
}

func (d *DB) plagiarismSourceVersion(assignmentID string) (int64, int64, error) {
	var lastSubmission, lastFile int64
	if err := d.conn.QueryRow(`SELECT COALESCE(MAX(id), 0) FROM submissions WHERE assignment_id=? AND is_latest=1`, assignmentID).Scan(&lastSubmission); err != nil {
		return 0, 0, err
	}
	if err := d.conn.QueryRow(`SELECT COALESCE(MAX(sf.id), 0) FROM submission_files sf
		JOIN submissions s ON s.id=sf.submission_id WHERE sf.assignment_id=? AND s.is_latest=1`, assignmentID).Scan(&lastFile); err != nil {
		return 0, 0, err
	}
	return lastSubmission, lastFile, nil
}
