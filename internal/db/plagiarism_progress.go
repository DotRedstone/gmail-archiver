package db

import (
	"database/sql"
	"fmt"
	"time"
)

type PlagiarismIndexWork struct {
	SubmissionID int64
	StudentID    string
	StoragePath  string
}

func (d *DB) ListPlagiarismIndexWork(assignmentID string, limit int) ([]PlagiarismIndexWork, error) {
	if limit < 1 || limit > 20 {
		return nil, fmt.Errorf("db: invalid plagiarism reindex limit")
	}
	staleBefore := time.Now().UTC().Add(-10 * time.Minute).Format(time.RFC3339Nano)
	rows, err := d.conn.Query(`SELECT s.id, s.student_id, a.storage_path FROM submissions s
		JOIN attachments a ON a.id=s.attachment_id
		LEFT JOIN plagiarism_index_progress p ON p.submission_id=s.id
		WHERE s.assignment_id=? AND s.is_latest=1 AND
		(p.submission_id IS NULL OR p.status='failed' OR (p.status='processing' AND p.updated_at<?))
		ORDER BY s.id LIMIT ?`, assignmentID, staleBefore, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var work []PlagiarismIndexWork
	for rows.Next() {
		var item PlagiarismIndexWork
		if err := rows.Scan(&item.SubmissionID, &item.StudentID, &item.StoragePath); err != nil {
			return nil, err
		}
		work = append(work, item)
	}
	return work, rows.Err()
}

func (d *DB) BeginPlagiarismReindex(submissionID int64, assignmentID string) error {
	tx, err := d.conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var latest bool
	if err := tx.QueryRow(`SELECT is_latest FROM submissions WHERE id=? AND assignment_id=?`, submissionID, assignmentID).Scan(&latest); err != nil {
		if err == sql.ErrNoRows {
			return ErrNotFound
		}
		return err
	}
	if !latest {
		return ErrNotFound
	}
	if _, err := tx.Exec(`DELETE FROM submission_files WHERE submission_id=?`, submissionID); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO plagiarism_index_progress (submission_id, assignment_id, status, failure_category, updated_at)
		VALUES (?, ?, 'processing', '', ?)
		ON CONFLICT(submission_id) DO UPDATE SET status='processing', failure_category='', updated_at=excluded.updated_at`,
		submissionID, assignmentID, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	return tx.Commit()
}

type PlagiarismProgress struct {
	Processing        int            `json:"processing"`
	Failed            int            `json:"failed"`
	Complete          int            `json:"complete"`
	FailureCategories map[string]int `json:"failure_categories,omitempty"`
}

// Progress is attached to each submission version. Only current versions
// determine whether a report may be called complete.
func (d *DB) AssignmentPlagiarismProgress(assignmentID string) (PlagiarismProgress, error) {
	var result PlagiarismProgress
	err := d.conn.QueryRow(`SELECT
		COALESCE(SUM(CASE WHEN p.status='processing' THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN p.status='failed' OR p.submission_id IS NULL THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN p.status='complete' THEN 1 ELSE 0 END), 0)
		FROM submissions s LEFT JOIN plagiarism_index_progress p ON p.submission_id=s.id
		WHERE s.assignment_id=? AND s.is_latest=1`, assignmentID).
		Scan(&result.Processing, &result.Failed, &result.Complete)
	if err != nil {
		return result, err
	}
	rows, err := d.conn.Query(`SELECT COALESCE(p.failure_category, 'legacy_unindexed'), COUNT(*)
		FROM submissions s LEFT JOIN plagiarism_index_progress p ON p.submission_id=s.id
		WHERE s.assignment_id=? AND s.is_latest=1 AND (p.status='failed' OR p.submission_id IS NULL)
		GROUP BY COALESCE(p.failure_category, 'legacy_unindexed')`, assignmentID)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	result.FailureCategories = make(map[string]int)
	for rows.Next() {
		var category string
		var count int
		if err := rows.Scan(&category, &count); err != nil {
			return result, err
		}
		switch category {
		case "extraction_failed", "index_write_failed", "storage_unavailable", "legacy_unindexed", "progress_write_failed":
		default:
			category = "other"
		}
		result.FailureCategories[category] += count
	}
	return result, rows.Err()
}

func (d *DB) SetPlagiarismIndexProgress(submissionID int64, status, category string) error {
	if status != "complete" && status != "failed" {
		return fmt.Errorf("db: invalid plagiarism progress status")
	}
	if status == "complete" {
		category = ""
	} else if category == "" {
		return fmt.Errorf("db: missing plagiarism failure category")
	}
	result, err := d.conn.Exec(`UPDATE plagiarism_index_progress SET status=?, failure_category=?, updated_at=?
		WHERE submission_id=?`, status, category, time.Now().UTC().Format(time.RFC3339Nano), submissionID)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return ErrNotFound
	}
	return nil
}
