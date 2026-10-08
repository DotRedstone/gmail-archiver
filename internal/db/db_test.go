package db

import (
	"os"
	"testing"
	"time"
)

// [Test]
func TestDBOperations(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "db_test_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	database, err := Open(tmpDir)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer database.Close()

	now := time.Now().UTC().Truncate(time.Second)

	att1 := &Attachment{
		MessageID:   "<msg1@example.com>",
		Sender:      "Alice <alice@example.com>",
		Subject:     "Monthly Report",
		ReceivedAt:  now.Add(-2 * time.Hour),
		Filename:    "report.pdf",
		FileSize:    1024,
		SHA256:      "hash1",
		MIMEType:    "application/pdf",
		StoragePath: "attachments/2026/10/report.pdf",
	}

	id1, err := database.InsertAttachment(att1)
	if err != nil {
		t.Fatalf("InsertAttachment() error = %v", err)
	}
	if id1 <= 0 {
		t.Errorf("expected positive ID, got %d", id1)
	}

	att2 := &Attachment{
		MessageID:   "<msg2@example.com>",
		Sender:      "Bob <bob@example.com>",
		Subject:     "Invoice Document",
		ReceivedAt:  now.Add(-1 * time.Hour),
		Filename:    "invoice.pdf",
		FileSize:    2048,
		SHA256:      "hash2",
		MIMEType:    "application/pdf",
		StoragePath: "attachments/2026/10/invoice.pdf",
	}
	_, err = database.InsertAttachment(att2)
	if err != nil {
		t.Fatalf("InsertAttachment() error = %v", err)
	}

	// Test GetAttachmentByID
	got, err := database.GetAttachmentByID(id1)
	if err != nil {
		t.Fatalf("GetAttachmentByID() error = %v", err)
	}
	if got.Filename != "report.pdf" || got.MessageID != "<msg1@example.com>" {
		t.Errorf("unexpected attachment: %+v", got)
	}

	// Test Exists
	exists, err := database.ExistsByMessageAndFilename("<msg1@example.com>", "report.pdf")
	if err != nil || !exists {
		t.Errorf("expected exists=true, got %v, err=%v", exists, err)
	}

	notExists, err := database.ExistsByMessageAndFilename("<msg1@example.com>", "nonexistent.pdf")
	if err != nil || notExists {
		t.Errorf("expected notExists=false, got %v, err=%v", notExists, err)
	}

	// Test ListAttachments with filters
	list, total, err := database.ListAttachments(AttachmentFilter{
		Keyword: "report",
		Page:    1,
		Limit:   10,
	})
	if err != nil {
		t.Fatalf("ListAttachments() error = %v", err)
	}
	if total != 1 || len(list) != 1 || list[0].Filename != "report.pdf" {
		t.Errorf("unexpected list result for keyword filter: total=%d, len=%d", total, len(list))
	}

	// Test UID sync state
	uid, err := database.GetLastSyncedUID("INBOX")
	if err != nil || uid != 0 {
		t.Errorf("expected initial UID 0, got %d, err=%v", uid, err)
	}

	if err := database.SetLastSyncedUID("INBOX", 42); err != nil {
		t.Fatalf("SetLastSyncedUID() error = %v", err)
	}

	uid, err = database.GetLastSyncedUID("INBOX")
	if err != nil || uid != 42 {
		t.Errorf("expected updated UID 42, got %d, err=%v", uid, err)
	}
}
