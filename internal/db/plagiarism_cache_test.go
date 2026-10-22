package db

import (
	"errors"
	"testing"
	"time"
)

func TestMaterializedPlagiarismReportRefreshesAfterNewSubmission(t *testing.T) {
	database, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	const assignment = "example_lab"
	if report, err := database.GetMaterializedPlagiarismReport(assignment); err != nil || len(report) != 0 {
		t.Fatalf("initial cache: report=%d err=%v", len(report), err)
	}
	for index, student := range []struct{ id, name string }{
		{"990000000001", "示例甲"}, {"990000000002", "示例乙"},
	} {
		attachment := &Attachment{
			MessageID: student.id, Sender: "example@example.invalid", Subject: "示例实验",
			ReceivedAt: time.Now().UTC(), Filename: "example.zip", FileSize: 3,
			SHA256: "same-synthetic-hash", MIMEType: "application/zip",
			StoragePath: "attachments/synthetic.zip",
		}
		attachmentID, err := database.InsertAttachment(attachment)
		if err != nil {
			t.Fatal(err)
		}
		submission := &Submission{
			AssignmentID: assignment, StudentID: student.id, StudentName: student.name,
			ClassName: "示例班A", AttachmentID: attachmentID, SubmittedAt: time.Now().UTC(),
			TargetFilename: "example.zip",
		}
		if _, _, err := database.RecordSubmission(submission); err != nil {
			t.Fatal(err)
		}
		if _, err := database.GetMaterializedPlagiarismReport(assignment); !errors.Is(err, ErrPlagiarismIncomplete) {
			t.Fatalf("unindexed submission was presented as complete: %v", err)
		}
		if err := database.SetPlagiarismIndexProgress(submission.ID, "complete", ""); err != nil {
			t.Fatal(err)
		}
		report, err := database.GetMaterializedPlagiarismReport(assignment)
		if err != nil || len(report) != index {
			t.Fatalf("after submission %d: report=%d err=%v", index+1, len(report), err)
		}
	}
	var stored string
	if err := database.conn.QueryRow(`SELECT report_json FROM plagiarism_report_cache WHERE assignment_id=?`, assignment).Scan(&stored); err != nil || stored == "" || stored == "null" {
		t.Fatalf("materialized report not stored: %v", err)
	}
}
