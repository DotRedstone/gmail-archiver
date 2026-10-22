package db

import (
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

var ErrLabCardConfirmed = errors.New("lab card already confirmed")
var ErrLabCardExists = errors.New("lab card draft already exists")

type LabCardDraft struct {
	Number            int                 `json:"number"`
	DraftID           string              `json:"draft_id"`
	Filename          string              `json:"filename"`
	StoragePath       string              `json:"-"`
	SHA256            string              `json:"sha256"`
	FileSize          int64               `json:"file_size"`
	SuggestedDeadline time.Time           `json:"suggested_deadline"`
	Deadline          time.Time           `json:"deadline"`
	Targets           []PublicationTarget `json:"targets"`
	ExpectedCount     int                 `json:"expected_count"`
	RosterSnapshot    []SnapshotStudent   `json:"-"`
	Status            string              `json:"status"`
	DeliveryStatus    string              `json:"delivery_status"`
	UploadedAt        time.Time           `json:"uploaded_at"`
	ConfirmedAt       *time.Time          `json:"confirmed_at,omitempty"`
}

type PublicationTarget struct {
	PlatformID    string   `json:"platform_id"`
	BotID         string   `json:"bot_id"`
	GroupOpenID   string   `json:"group_openid"`
	ClassNames    []string `json:"class_names"`
	ExpectedCount int      `json:"expected_count"`
}

type SnapshotStudent struct {
	StudentID   string `json:"student_id"`
	StudentName string `json:"student_name"`
	ClassName   string `json:"class_name"`
}

func (d *DB) SaveLabCardDraft(card LabCardDraft) error {
	targets, err := json.Marshal(card.Targets)
	if err != nil {
		return err
	}
	snapshot, err := json.Marshal(card.RosterSnapshot)
	if err != nil {
		return err
	}
	result, err := d.conn.Exec(`INSERT INTO lab_card_drafts
		(number, draft_id, filename, storage_path, sha256, file_size, suggested_deadline, deadline,
		targets_json, roster_snapshot_json, expected_count, status, delivery_status, uploaded_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'draft', 'not_started', ?)
		ON CONFLICT(number) DO NOTHING`,
		card.Number, card.DraftID, card.Filename, card.StoragePath, card.SHA256, card.FileSize,
		card.SuggestedDeadline.UTC().Format(time.RFC3339Nano), card.Deadline.UTC().Format(time.RFC3339Nano),
		string(targets), string(snapshot), card.ExpectedCount, card.UploadedAt.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed == 0 {
		return ErrLabCardExists
	}
	return nil
}

func (d *DB) GetLabCardDraft(number int) (*LabCardDraft, error) {
	var card LabCardDraft
	var suggested, deadline, uploaded, targets, snapshot string
	var confirmed sql.NullString
	err := d.conn.QueryRow(`SELECT number, draft_id, filename, storage_path, sha256, file_size,
		suggested_deadline, deadline, targets_json, roster_snapshot_json, expected_count,
		status, delivery_status, uploaded_at, confirmed_at
		FROM lab_card_drafts WHERE number=?`, number).
		Scan(&card.Number, &card.DraftID, &card.Filename, &card.StoragePath, &card.SHA256, &card.FileSize,
			&suggested, &deadline, &targets, &snapshot, &card.ExpectedCount,
			&card.Status, &card.DeliveryStatus, &uploaded, &confirmed)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	card.SuggestedDeadline, err = time.Parse(time.RFC3339Nano, suggested)
	if err != nil {
		return nil, err
	}
	if deadline != "" {
		card.Deadline, err = time.Parse(time.RFC3339Nano, deadline)
		if err != nil {
			return nil, err
		}
	} else {
		card.Deadline = card.SuggestedDeadline
	}
	if err := json.Unmarshal([]byte(targets), &card.Targets); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(snapshot), &card.RosterSnapshot); err != nil {
		return nil, err
	}
	card.UploadedAt, err = time.Parse(time.RFC3339Nano, uploaded)
	if err != nil {
		return nil, err
	}
	if confirmed.Valid {
		at, parseErr := time.Parse(time.RFC3339Nano, confirmed.String)
		if parseErr != nil {
			return nil, parseErr
		}
		card.ConfirmedAt = &at
	}
	return &card, nil
}

func (d *DB) GetLabCardDraftByID(draftID string) (*LabCardDraft, error) {
	var number int
	if err := d.conn.QueryRow(`SELECT number FROM lab_card_drafts WHERE draft_id=?`, draftID).Scan(&number); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return d.GetLabCardDraft(number)
}

func (d *DB) ConfirmLabCard(number int, now time.Time) error {
	result, err := d.conn.Exec(`UPDATE lab_card_drafts SET status='confirmed', delivery_status='manual_pending', confirmed_at=?
		WHERE number=? AND status='draft'`, now.UTC().Format(time.RFC3339Nano), number)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed == 0 {
		return ErrNotFound
	}
	return nil
}

func (d *DB) UpdateLabCardScope(card LabCardDraft) error {
	targets, err := json.Marshal(card.Targets)
	if err != nil {
		return err
	}
	snapshot, err := json.Marshal(card.RosterSnapshot)
	if err != nil {
		return err
	}
	result, err := d.conn.Exec(`UPDATE lab_card_drafts SET deadline=?, targets_json=?, roster_snapshot_json=?, expected_count=?
		WHERE number=? AND draft_id=? AND status='draft'`,
		card.Deadline.UTC().Format(time.RFC3339Nano), string(targets), string(snapshot), card.ExpectedCount,
		card.Number, card.DraftID)
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

func (d *DB) CancelLabCardDraft(number int, draftID string) error {
	result, err := d.conn.Exec(`DELETE FROM lab_card_drafts WHERE number=? AND draft_id=? AND status='draft'`, number, draftID)
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
