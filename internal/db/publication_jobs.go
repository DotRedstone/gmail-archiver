package db

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"
)

type PublicationJob struct {
	JobID           string    `json:"job_id"`
	DraftID         string    `json:"draft_id"`
	AssignmentID    string    `json:"assignment_id"`
	PlatformID      string    `json:"platform_id"`
	BotID           string    `json:"bot_id"`
	GroupOpenID     string    `json:"group_openid"`
	ClassNames      []string  `json:"class_names"`
	Kind            string    `json:"kind"`
	DueAt           time.Time `json:"due_at"`
	Status          string    `json:"status"`
	MessageID       string    `json:"message_id,omitempty"`
	FailureCategory string    `json:"failure_category,omitempty"`
	UpdatedAt       time.Time `json:"updated_at"`
}

func publicationJobID(draftID string, target PublicationTarget, kind string) string {
	sum := sha256.Sum256([]byte(draftID + "\x00" + target.PlatformID + "\x00" + target.BotID + "\x00" + target.GroupOpenID + "\x00" + kind))
	return "J" + hex.EncodeToString(sum[:12])
}

func (d *DB) ConfirmLabCardWithJobs(card LabCardDraft, assignmentID string, now time.Time) ([]PublicationJob, error) {
	tx, err := d.conn.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO published_assignments (assignment_id, draft_id, status, created_at)
		VALUES (?, ?, 'preparing', ?)`, assignmentID, card.DraftID, now.UTC().Format(time.RFC3339Nano)); err != nil {
		return nil, err
	}
	result, err := tx.Exec(`UPDATE lab_card_drafts SET status='confirmed', delivery_status='not_started', confirmed_at=?
		WHERE number=? AND draft_id=? AND status='draft'`, now.UTC().Format(time.RFC3339Nano), card.Number, card.DraftID)
	if err != nil {
		return nil, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	if count == 0 {
		return nil, ErrNotFound
	}
	var jobs []PublicationJob
	for _, target := range card.Targets {
		classes, err := json.Marshal(target.ClassNames)
		if err != nil {
			return nil, err
		}
		for _, kind := range []string{"file", "notice", "reminder"} {
			due := now.UTC()
			if kind == "reminder" {
				due = card.Deadline.Add(-24 * time.Hour).UTC()
			}
			job := PublicationJob{
				JobID: publicationJobID(card.DraftID, target, kind), DraftID: card.DraftID,
				AssignmentID: assignmentID, PlatformID: target.PlatformID, BotID: target.BotID,
				GroupOpenID: target.GroupOpenID, ClassNames: target.ClassNames,
				Kind: kind, DueAt: due, Status: "pending", UpdatedAt: now.UTC(),
			}
			_, err = tx.Exec(`INSERT INTO publication_delivery_jobs
				(job_id, draft_id, assignment_id, platform_id, bot_id, group_openid, class_names_json,
				kind, due_at, status, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'pending', ?)`,
				job.JobID, job.DraftID, job.AssignmentID, job.PlatformID, job.BotID, job.GroupOpenID,
				string(classes), job.Kind, job.DueAt.Format(time.RFC3339Nano), now.UTC().Format(time.RFC3339Nano))
			if err != nil {
				return nil, err
			}
			jobs = append(jobs, job)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return jobs, nil
}

func scanPublicationJob(row interface{ Scan(...any) error }) (*PublicationJob, error) {
	var job PublicationJob
	var classes, due, updated string
	err := row.Scan(&job.JobID, &job.DraftID, &job.AssignmentID, &job.PlatformID, &job.BotID,
		&job.GroupOpenID, &classes, &job.Kind, &due, &job.Status, &job.MessageID,
		&job.FailureCategory, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(classes), &job.ClassNames); err != nil {
		return nil, err
	}
	job.DueAt, err = time.Parse(time.RFC3339Nano, due)
	if err != nil {
		return nil, err
	}
	job.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated)
	return &job, err
}

const publicationJobColumns = `job_id, draft_id, assignment_id, platform_id, bot_id, group_openid, class_names_json, kind, due_at, status, message_id, failure_category, updated_at`

func (d *DB) GetPublicationJob(id string) (*PublicationJob, error) {
	return scanPublicationJob(d.conn.QueryRow(`SELECT `+publicationJobColumns+` FROM publication_delivery_jobs WHERE job_id=?`, id))
}

func (d *DB) ListPublicationJobs(draftID string) ([]*PublicationJob, error) {
	rows, err := d.conn.Query(`SELECT `+publicationJobColumns+` FROM publication_delivery_jobs WHERE draft_id=? ORDER BY group_openid, kind`, draftID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var jobs []*PublicationJob
	for rows.Next() {
		job, err := scanPublicationJob(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}

func (d *DB) DuePublicationJobs(platformID, botID string, now time.Time) ([]*PublicationJob, error) {
	rows, err := d.conn.Query(`SELECT `+publicationJobColumns+` FROM publication_delivery_jobs j
		WHERE j.platform_id=? AND j.bot_id=? AND j.status='pending' AND j.due_at<=?
		AND EXISTS (SELECT 1 FROM published_assignments p WHERE p.assignment_id=j.assignment_id AND p.status='ready')
		ORDER BY j.due_at, CASE j.kind WHEN 'file' THEN 0 WHEN 'notice' THEN 1 ELSE 2 END, j.job_id LIMIT 50`,
		platformID, botID, now.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var jobs []*PublicationJob
	for rows.Next() {
		job, err := scanPublicationJob(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}

// Claiming moves a job to unknown before an external send. A crash therefore
// cannot make the next poll blindly repeat a possibly delivered QQ message.
func (d *DB) ClaimPublicationJob(id, platformID, botID string, now time.Time) (*PublicationJob, error) {
	result, err := d.conn.Exec(`UPDATE publication_delivery_jobs SET status='unknown', failure_category='interrupted', updated_at=?
		WHERE job_id=? AND platform_id=? AND bot_id=? AND status='pending' AND due_at<=?
		AND EXISTS (SELECT 1 FROM published_assignments p WHERE p.assignment_id=publication_delivery_jobs.assignment_id AND p.status='ready')`,
		now.UTC().Format(time.RFC3339Nano), id, platformID, botID, now.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return nil, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	if count == 0 {
		return nil, ErrNotFound
	}
	return d.GetPublicationJob(id)
}

func (d *DB) MarkPublishedAssignmentReady(assignmentID, draftID string, now time.Time) error {
	tx, err := d.conn.Begin()
	if err != nil { return err }
	defer tx.Rollback()
	var status string
	if err := tx.QueryRow(`SELECT status FROM published_assignments WHERE assignment_id=? AND draft_id=?`, assignmentID, draftID).Scan(&status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if status == "preparing" {
		stamp := now.UTC().Format(time.RFC3339Nano)
		if _, err := tx.Exec(`UPDATE publication_delivery_jobs SET status='expired', failure_category='deadline_passed', updated_at=?
			WHERE draft_id=? AND kind='reminder' AND status='pending' AND due_at<=?`, stamp, draftID, stamp); err != nil { return err }
		if _, err := tx.Exec(`UPDATE published_assignments SET status='ready', ready_at=?
			WHERE assignment_id=? AND draft_id=? AND status='preparing'`, stamp, assignmentID, draftID); err != nil { return err }
	}
	return tx.Commit()
}

func (d *DB) PublishedAssignmentStatus(assignmentID, draftID string) (string, error) {
	var status string
	err := d.conn.QueryRow(`SELECT status FROM published_assignments WHERE assignment_id=? AND draft_id=?`, assignmentID, draftID).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return status, err
}

func (d *DB) RecordPublicationResult(id, platformID, botID, status, messageID, category string, now time.Time) (*PublicationJob, error) {
	result, err := d.conn.Exec(`UPDATE publication_delivery_jobs
		SET status=?, message_id=?, failure_category=?, updated_at=?
		WHERE job_id=? AND platform_id=? AND bot_id=? AND status='unknown'`,
		status, messageID, category, now.UTC().Format(time.RFC3339Nano), id, platformID, botID)
	if err != nil {
		return nil, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	if count == 0 {
		return nil, ErrNotFound
	}
	return d.GetPublicationJob(id)
}

func (d *DB) HasPublishedAssignment(assignmentID string) (bool, error) {
	var exists int
	err := d.conn.QueryRow(`SELECT EXISTS(SELECT 1 FROM published_assignments WHERE assignment_id=?)`, assignmentID).Scan(&exists)
	return exists != 0, err
}
