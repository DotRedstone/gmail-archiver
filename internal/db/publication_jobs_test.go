package db

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func TestLegacyConfirmedCardDoesNotCreateDeliveryJobs(t *testing.T) {
	root := t.TempDir()
	legacy, err := sql.Open("sqlite", filepath.Join(root, "metadata.db"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = legacy.Exec(`CREATE TABLE lab_card_drafts (
		number INTEGER PRIMARY KEY, filename TEXT NOT NULL, storage_path TEXT NOT NULL,
		sha256 TEXT NOT NULL, file_size INTEGER NOT NULL, suggested_deadline DATETIME NOT NULL,
		status TEXT NOT NULL CHECK(status IN ('draft','confirmed')),
		delivery_status TEXT NOT NULL CHECK(delivery_status IN ('not_started','manual_pending','sent','failed')),
		uploaded_at DATETIME NOT NULL, confirmed_at DATETIME
	)`)
	if err != nil {
		t.Fatal(err)
	}
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = legacy.Exec(`INSERT INTO lab_card_drafts (number, filename, storage_path, sha256, file_size,
		suggested_deadline, status, delivery_status, uploaded_at, confirmed_at)
		VALUES (3, '实验卡3.pdf', 'attachments/synthetic.pdf', 'synthetic', 12, ?, 'confirmed', 'manual_pending', ?, ?)`, stamp, stamp, stamp)
	if err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}
	database, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	card, err := database.GetLabCardDraft(3)
	if err != nil {
		t.Fatal(err)
	}
	if card.DraftID != "" || card.DeliveryStatus != "manual_pending" || card.Status != "confirmed" {
		t.Fatalf("legacy card changed on migration: %+v", card)
	}
	jobs, err := database.DuePublicationJobs("course-qq", "bot-example", time.Now().UTC())
	if err != nil || len(jobs) != 0 {
		t.Fatalf("legacy card unexpectedly scheduled jobs: %d, %v", len(jobs), err)
	}
}
