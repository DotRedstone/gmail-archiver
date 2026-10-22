package db

import (
	"database/sql"
	"errors"
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

func TestPublicationDeliveryStatesPersistAcrossRestart(t *testing.T) {
	root := t.TempDir()
	database, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	card := LabCardDraft{
		Number: 8, DraftID: "D08-synthetic", Filename: "实验卡8.pdf",
		StoragePath: "attachments/synthetic.pdf", SHA256: "synthetic",
		FileSize: 20, SuggestedDeadline: now.Add(72 * time.Hour),
		Deadline: now.Add(72 * time.Hour), UploadedAt: now,
		Targets: []PublicationTarget{
			{PlatformID: "course-qq", BotID: "bot-example", GroupOpenID: "group-one", ClassNames: []string{"示例班A"}, ExpectedCount: 1},
			{PlatformID: "course-qq", BotID: "bot-example", GroupOpenID: "group-two", ClassNames: []string{"示例班A"}, ExpectedCount: 1},
		},
		RosterSnapshot: []SnapshotStudent{{StudentID: "990000000001", StudentName: "示例甲", ClassName: "示例班A"}},
		ExpectedCount: 1,
	}
	if err := database.SaveLabCardDraft(card); err != nil {
		t.Fatal(err)
	}
	jobs, err := database.ConfirmLabCardWithJobs(card, "experiment_8", now)
	if err != nil || len(jobs) != 6 {
		t.Fatalf("job transaction incomplete: %d %v", len(jobs), err)
	}
	var first, second string
	for _, job := range jobs {
		if job.Kind == "file" && job.GroupOpenID == "group-one" {
			first = job.JobID
		}
		if job.Kind == "file" && job.GroupOpenID == "group-two" {
			second = job.JobID
		}
	}
	if first == "" || second == "" {
		t.Fatal("missing per-group file delivery jobs")
	}
	if _, err := database.ClaimPublicationJob(first, "course-qq", "bot-example", now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("job was claimed before projection ready: %v", err)
	}
	if err := database.MarkPublishedAssignmentReady("experiment_8", card.DraftID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ClaimPublicationJob(first, "course-qq", "wrong-bot", now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other bot claimed job: %v", err)
	}
	claimed, err := database.ClaimPublicationJob(first, "course-qq", "bot-example", now)
	if err != nil || claimed.Status != "unknown" || claimed.FailureCategory != "interrupted" {
		t.Fatalf("claim did not persist ambiguous send boundary: %+v %v", claimed, err)
	}
	if _, err := database.ClaimPublicationJob(first, "course-qq", "bot-example", now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second claim was not rejected: %v", err)
	}
	failed, err := database.RecordPublicationResult(first, "course-qq", "bot-example", "failed", "", "quota", now)
	if err != nil || failed.Status != "failed" || failed.FailureCategory != "quota" {
		t.Fatalf("failure receipt not durable: %+v %v", failed, err)
	}
	if _, err := database.RecordPublicationResult(first, "course-qq", "bot-example", "sent", "forged", "", now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("failure was overwritten with fake sent state: %v", err)
	}
	claimed, err = database.ClaimPublicationJob(second, "course-qq", "bot-example", now)
	if err != nil || claimed.Status != "unknown" {
		t.Fatalf("second group could not be claimed: %+v %v", claimed, err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	for _, item := range []struct{ id, status, category string }{
		{first, "failed", "quota"}, {second, "unknown", "interrupted"},
	} {
		got, err := reopened.GetPublicationJob(item.id)
		if err != nil || got.Status != item.status || got.FailureCategory != item.category {
			t.Fatalf("restart changed delivery state %s: %+v %v", item.id, got, err)
		}
	}
	due, err := reopened.DuePublicationJobs("course-qq", "bot-example", now)
	if err != nil {
		t.Fatal(err)
	}
	for _, job := range due {
		if job.JobID == first || job.JobID == second {
			t.Fatalf("failed/unknown job automatically requeued: %+v", job)
		}
	}
	if err := reopened.MarkPublishedAssignmentReady("experiment_8", card.DraftID, now); err != nil {
		t.Fatal(err)
	}
	if again, err := reopened.ListPublicationJobs(card.DraftID); err != nil || len(again) != 6 {
		t.Fatalf("repeated readiness duplicated jobs: %d %v", len(again), err)
	}
}
