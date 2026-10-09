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

// [TestSubmissionVersioning]
func TestSubmissionVersioningAndOverwrite(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "db_sub_test_*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	database, err := Open(tmpDir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer database.Close()

	now := time.Now().UTC()

	// 1. Create dummy attachment records
	att1 := &Attachment{
		MessageID:   "<msg1@example.com>",
		Sender:      "支全振 <zhi@example.com>",
		Subject:     "并行计算-实验1-支全振",
		ReceivedAt:  now.Add(-2 * time.Hour),
		Filename:    "实验1-241-240809010501-支全振.zip",
		FileSize:    1024,
		SHA256:      "hash-v1",
		MIMEType:    "application/zip",
		StoragePath: "attachments/2026/09/hash-v1_test.zip",
	}
	attID1, err := database.InsertAttachment(att1)
	if err != nil {
		t.Fatalf("InsertAttachment 1: %v", err)
	}

	att2 := &Attachment{
		MessageID:   "<msg2@example.com>",
		Sender:      "支全振 <zhi@example.com>",
		Subject:     "并行计算-实验1-支全振（更正版）",
		ReceivedAt:  now.Add(-1 * time.Hour),
		Filename:    "实验1-241-240809010501-支全振_v2.zip",
		FileSize:    2048,
		SHA256:      "hash-v2",
		MIMEType:    "application/zip",
		StoragePath: "attachments/2026/09/hash-v2_test.zip",
	}
	attID2, err := database.InsertAttachment(att2)
	if err != nil {
		t.Fatalf("InsertAttachment 2: %v", err)
	}

	// 2. First submission (v1)
	sub1 := &Submission{
		AssignmentID:   "parallel_computing_lab1",
		StudentID:      "240809010501",
		StudentName:    "支全振",
		ClassName:      "2024级计算机科学与技术5班",
		AttachmentID:   attID1,
		SubmittedAt:    now.Add(-2 * time.Hour),
		TargetFilename: "实验1-241-240809010501-支全振.zip",
	}
	v1, isUpdate1, err := database.RecordSubmission(sub1)
	if err != nil {
		t.Fatalf("RecordSubmission 1: %v", err)
	}
	if v1 != 1 || isUpdate1 {
		t.Errorf("expected v1 initial submission, got v=%d, isUpdate=%v", v1, isUpdate1)
	}

	// 3. Second submission (correction v2)
	sub2 := &Submission{
		AssignmentID:   "parallel_computing_lab1",
		StudentID:      "240809010501",
		StudentName:    "支全振",
		ClassName:      "2024级计算机科学与技术5班",
		AttachmentID:   attID2,
		SubmittedAt:    now.Add(-1 * time.Hour),
		TargetFilename: "实验1-241-240809010501-支全振.zip",
	}
	v2, isUpdate2, err := database.RecordSubmission(sub2)
	if err != nil {
		t.Fatalf("RecordSubmission 2: %v", err)
	}
	if v2 != 2 || !isUpdate2 {
		t.Errorf("expected v2 update submission, got v=%d, isUpdate=%v", v2, isUpdate2)
	}

	// 4. Verify LatestSubmissions contains ONLY v2
	latestList, err := database.GetLatestSubmissions("parallel_computing_lab1")
	if err != nil {
		t.Fatalf("GetLatestSubmissions: %v", err)
	}
	if len(latestList) != 1 {
		t.Fatalf("expected 1 latest submission, got %d", len(latestList))
	}
	if latestList[0].Version != 2 || latestList[0].AttachmentID != attID2 || latestList[0].SHA256 != "hash-v2" {
		t.Errorf("latest submission mismatch: %+v", latestList[0])
	}

	// 5. Verify StudentSubmissionHistory contains both v2 and v1 in descending order
	history, err := database.GetStudentSubmissionHistory("parallel_computing_lab1", "240809010501")
	if err != nil {
		t.Fatalf("GetStudentSubmissionHistory: %v", err)
	}
	if len(history) != 2 {
		t.Fatalf("expected 2 history submissions, got %d", len(history))
	}
	if history[0].Version != 2 || !history[0].IsLatest {
		t.Errorf("history[0] should be v2 latest, got %+v", history[0])
	}
	if history[1].Version != 1 || history[1].IsLatest {
		t.Errorf("history[1] should be v1 non-latest, got %+v", history[1])
	}
}

func TestStudentBindings(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "db_binding_test_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	database, err := Open(tmpDir)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer database.Close()

	// 1. Initial lookup not found
	_, err = database.GetStudentBindingByQQ("123456789")
	if err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}

	// 2. Insert binding
	b := &StudentBinding{
		QQID:        "123456789",
		StudentID:   "240809010501",
		StudentName: "支全振",
		ClassName:   "2024级计算机科学与技术5班",
	}
	if err := database.UpsertStudentBinding(b); err != nil {
		t.Fatalf("UpsertStudentBinding: %v", err)
	}

	// 3. Query by QQ and Student ID
	got, err := database.GetStudentBindingByQQ("123456789")
	if err != nil {
		t.Fatalf("GetStudentBindingByQQ: %v", err)
	}
	if got.StudentID != "240809010501" || got.StudentName != "支全振" {
		t.Errorf("unexpected binding: %+v", got)
	}

	gotByID, err := database.GetStudentBindingByStudentID("240809010501")
	if err != nil {
		t.Fatalf("GetStudentBindingByStudentID: %v", err)
	}
	if gotByID.QQID != "123456789" {
		t.Errorf("unexpected binding by student id: %+v", gotByID)
	}

	// 4. Update binding (Upsert on same QQ)
	b.StudentID = "240809010502"
	b.StudentName = "王五"
	if err := database.UpsertStudentBinding(b); err != nil {
		t.Fatalf("UpsertStudentBinding update: %v", err)
	}
	gotUpdated, err := database.GetStudentBindingByQQ("123456789")
	if err != nil || gotUpdated.StudentID != "240809010502" {
		t.Errorf("expected updated student_id, got %+v", gotUpdated)
	}

	// 5. List bindings
	list, err := database.ListStudentBindings()
	if err != nil || len(list) != 1 {
		t.Fatalf("expected 1 binding in list, got len=%d, err=%v", len(list), err)
	}

	// 6. Delete binding
	if err := database.DeleteStudentBinding("123456789"); err != nil {
		t.Fatalf("DeleteStudentBinding: %v", err)
	}
	_, err = database.GetStudentBindingByQQ("123456789")
	if err != ErrNotFound {
		t.Errorf("expected ErrNotFound after delete, got %v", err)
	}
}

