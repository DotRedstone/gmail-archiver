package db

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

var (
	ErrNotFound = errors.New("db: record not found")
)

// [Model]
type Attachment struct {
	ID          int64     `json:"id"`
	MessageID   string    `json:"message_id"`
	Sender      string    `json:"sender"`
	Subject     string    `json:"subject"`
	ReceivedAt  time.Time `json:"received_at"`
	Filename    string    `json:"filename"`
	FileSize    int64     `json:"file_size"`
	SHA256      string    `json:"sha256"`
	MIMEType    string    `json:"mime_type"`
	StoragePath string    `json:"storage_path"`
	CreatedAt   time.Time `json:"created_at"`
}

type Submission struct {
	ID             int64     `json:"id"`
	AssignmentID   string    `json:"assignment_id"`
	StudentID      string    `json:"student_id"`
	StudentName    string    `json:"student_name"`
	ClassName      string    `json:"class_name"`
	AttachmentID   int64     `json:"attachment_id"`
	SubmittedAt    time.Time `json:"submitted_at"`
	Version        int       `json:"version"`
	IsLatest       bool      `json:"is_latest"`
	IsLate         bool      `json:"is_late"`
	TargetFilename string    `json:"target_filename"`
	CreatedAt      time.Time `json:"created_at"`

	// Joined attachment fields
	FileSize    int64  `json:"file_size,omitempty"`
	SHA256      string `json:"sha256,omitempty"`
	StoragePath string `json:"storage_path,omitempty"`
}

type StudentBinding struct {
	QQID        string    `json:"qq_id"`
	StudentID   string    `json:"student_id"`
	StudentName string    `json:"student_name"`
	ClassName   string    `json:"class_name"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// [Filter]
type AttachmentFilter struct {
	Keyword  string
	Sender   string
	FromDate *time.Time
	ToDate   *time.Time
	Page     int
	Limit    int
}

// [Database]
type DB struct {
	conn *sql.DB
}

func Open(dataDir string) (*DB, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("db: create data dir: %w", err)
	}

	dbPath := filepath.Join(dataDir, "metadata.db")
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)", dbPath)

	conn, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("db: open sqlite: %w", err)
	}

	conn.SetMaxOpenConns(1) // SQLite single writer safety
	conn.SetMaxIdleConns(1)

	d := &DB{conn: conn}
	if err := d.migrate(); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("db: migrate: %w", err)
	}

	return d, nil
}

func (d *DB) Close() error {
	return d.conn.Close()
}

// [Migration]
func (d *DB) migrate() error {
	queries := []string{
		`CREATE TABLE IF NOT EXISTS attachments (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			message_id TEXT NOT NULL,
			sender TEXT NOT NULL,
			subject TEXT NOT NULL,
			received_at DATETIME NOT NULL,
			filename TEXT NOT NULL,
			file_size INTEGER NOT NULL,
			sha256 TEXT NOT NULL,
			mime_type TEXT NOT NULL,
			storage_path TEXT NOT NULL,
			created_at DATETIME NOT NULL
		);`,
		`CREATE INDEX IF NOT EXISTS idx_attachments_sha256 ON attachments(sha256);`,
		`CREATE INDEX IF NOT EXISTS idx_attachments_message_id ON attachments(message_id);`,
		`CREATE INDEX IF NOT EXISTS idx_attachments_received_at ON attachments(received_at);`,
		`CREATE INDEX IF NOT EXISTS idx_attachments_filename ON attachments(filename);`,
		`CREATE TABLE IF NOT EXISTS sync_state (
			key TEXT PRIMARY KEY,
			val TEXT NOT NULL,
			updated_at DATETIME NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS submissions (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			assignment_id TEXT NOT NULL,
			student_id TEXT NOT NULL,
			student_name TEXT NOT NULL,
			class_name TEXT NOT NULL,
			attachment_id INTEGER NOT NULL,
			submitted_at DATETIME NOT NULL,
			version INTEGER NOT NULL DEFAULT 1,
			is_latest BOOLEAN NOT NULL DEFAULT 1,
			is_late BOOLEAN NOT NULL DEFAULT 0,
			target_filename TEXT NOT NULL,
			created_at DATETIME NOT NULL,
			FOREIGN KEY(attachment_id) REFERENCES attachments(id)
		);`,
		`CREATE INDEX IF NOT EXISTS idx_submissions_lookup ON submissions(assignment_id, student_id);`,
		`CREATE INDEX IF NOT EXISTS idx_submissions_latest ON submissions(assignment_id, is_latest);`,
		`CREATE TABLE IF NOT EXISTS student_bindings (
			qq_id TEXT PRIMARY KEY,
			student_id TEXT NOT NULL,
			student_name TEXT NOT NULL,
			class_name TEXT NOT NULL,
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL
		);`,
		`CREATE INDEX IF NOT EXISTS idx_student_bindings_student_id ON student_bindings(student_id);`,
	}

	for _, q := range queries {
		if _, err := d.conn.Exec(q); err != nil {
			return err
		}
	}
	return nil
}

// [CRUD]
func (d *DB) InsertAttachment(att *Attachment) (int64, error) {
	query := `INSERT INTO attachments (
		message_id, sender, subject, received_at, filename, file_size, sha256, mime_type, storage_path, created_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

	now := time.Now().UTC()
	if att.CreatedAt.IsZero() {
		att.CreatedAt = now
	}

	res, err := d.conn.Exec(
		query,
		att.MessageID,
		att.Sender,
		att.Subject,
		att.ReceivedAt.UTC().Format(time.RFC3339),
		att.Filename,
		att.FileSize,
		att.SHA256,
		att.MIMEType,
		att.StoragePath,
		att.CreatedAt.UTC().Format(time.RFC3339),
	)
	if err != nil {
		return 0, fmt.Errorf("db: insert attachment: %w", err)
	}

	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	att.ID = id
	return id, nil
}

func (d *DB) GetAttachmentByID(id int64) (*Attachment, error) {
	query := `SELECT id, message_id, sender, subject, received_at, filename, file_size, sha256, mime_type, storage_path, created_at
		FROM attachments WHERE id = ?`

	var att Attachment
	var recvAtStr, createdAtStr string

	err := d.conn.QueryRow(query, id).Scan(
		&att.ID,
		&att.MessageID,
		&att.Sender,
		&att.Subject,
		&recvAtStr,
		&att.Filename,
		&att.FileSize,
		&att.SHA256,
		&att.MIMEType,
		&att.StoragePath,
		&createdAtStr,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("db: get attachment: %w", err)
	}

	att.ReceivedAt, _ = time.Parse(time.RFC3339, recvAtStr)
	att.CreatedAt, _ = time.Parse(time.RFC3339, createdAtStr)
	return &att, nil
}

func (d *DB) ExistsByMessageAndFilename(messageID, filename string) (bool, error) {
	_, exists, err := d.GetAttachmentIDByMessageAndFilename(messageID, filename)
	return exists, err
}

func (d *DB) GetAttachmentIDByMessageAndFilename(messageID, filename string) (int64, bool, error) {
	query := `SELECT id FROM attachments WHERE message_id = ? AND filename = ? LIMIT 1`
	var id int64
	err := d.conn.QueryRow(query, messageID, filename).Scan(&id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("db: exists query: %w", err)
	}
	return id, true, nil
}

func (d *DB) ListAttachments(filter AttachmentFilter) ([]*Attachment, int64, error) {
	var whereClauses []string
	var args []any

	if filter.Keyword != "" {
		kw := "%" + strings.TrimSpace(filter.Keyword) + "%"
		whereClauses = append(whereClauses, "(filename LIKE ? OR subject LIKE ?)")
		args = append(args, kw, kw)
	}

	if filter.Sender != "" {
		snd := "%" + strings.TrimSpace(filter.Sender) + "%"
		whereClauses = append(whereClauses, "sender LIKE ?")
		args = append(args, snd)
	}

	if filter.FromDate != nil {
		whereClauses = append(whereClauses, "received_at >= ?")
		args = append(args, filter.FromDate.UTC().Format(time.RFC3339))
	}

	if filter.ToDate != nil {
		whereClauses = append(whereClauses, "received_at <= ?")
		args = append(args, filter.ToDate.UTC().Format(time.RFC3339))
	}

	whereSQL := ""
	if len(whereClauses) > 0 {
		whereSQL = " WHERE " + strings.Join(whereClauses, " AND ")
	}

	// Count total records
	countSQL := "SELECT COUNT(*) FROM attachments" + whereSQL
	var total int64
	if err := d.conn.QueryRow(countSQL, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("db: count attachments: %w", err)
	}

	// Pagination
	limit := filter.Limit
	if limit <= 0 {
		limit = 20
	} else if limit > 100 {
		limit = 100
	}

	page := filter.Page
	if page <= 0 {
		page = 1
	}
	offset := (page - 1) * limit

	querySQL := "SELECT id, message_id, sender, subject, received_at, filename, file_size, sha256, mime_type, storage_path, created_at FROM attachments" +
		whereSQL + " ORDER BY received_at DESC, id DESC LIMIT ? OFFSET ?"

	queryArgs := append(args, limit, offset)
	rows, err := d.conn.Query(querySQL, queryArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("db: query attachments: %w", err)
	}
	defer rows.Close()

	var list []*Attachment
	for rows.Next() {
		var att Attachment
		var recvAtStr, createdAtStr string
		if err := rows.Scan(
			&att.ID,
			&att.MessageID,
			&att.Sender,
			&att.Subject,
			&recvAtStr,
			&att.Filename,
			&att.FileSize,
			&att.SHA256,
			&att.MIMEType,
			&att.StoragePath,
			&createdAtStr,
		); err != nil {
			return nil, 0, fmt.Errorf("db: scan attachment: %w", err)
		}
		att.ReceivedAt, _ = time.Parse(time.RFC3339, recvAtStr)
		att.CreatedAt, _ = time.Parse(time.RFC3339, createdAtStr)
		list = append(list, &att)
	}

	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("db: iterate attachments: %w", err)
	}

	return list, total, nil
}

// [SyncState]
func (d *DB) GetLastSyncedUID(mailbox string) (uint32, error) {
	key := "last_uid_" + mailbox
	query := `SELECT val FROM sync_state WHERE key = ?`
	var val string
	err := d.conn.QueryRow(query, key).Scan(&val)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, nil
		}
		return 0, fmt.Errorf("db: get sync state: %w", err)
	}
	u, err := strconv.ParseUint(val, 10, 32)
	if err != nil {
		return 0, nil
	}
	return uint32(u), nil
}

func (d *DB) SetLastSyncedUID(mailbox string, uid uint32) error {
	key := "last_uid_" + mailbox
	query := `INSERT INTO sync_state (key, val, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET val = excluded.val, updated_at = excluded.updated_at`

	now := time.Now().UTC().Format(time.RFC3339)
	_, err := d.conn.Exec(query, key, strconv.FormatUint(uint64(uid), 10), now)
	if err != nil {
		return fmt.Errorf("db: set sync state: %w", err)
	}
	return nil
}

// [RecordSubmission]
func (d *DB) RecordSubmission(sub *Submission) (version int, isUpdate bool, err error) {
	tx, err := d.conn.Begin()
	if err != nil {
		return 0, false, fmt.Errorf("db: begin tx: %w", err)
	}
	defer tx.Rollback()

	var maxVer sql.NullInt64
	err = tx.QueryRow(
		`SELECT MAX(version) FROM submissions WHERE assignment_id = ? AND student_id = ?`,
		sub.AssignmentID, sub.StudentID,
	).Scan(&maxVer)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, false, fmt.Errorf("db: query max version: %w", err)
	}

	newVersion := 1
	if maxVer.Valid && maxVer.Int64 > 0 {
		newVersion = int(maxVer.Int64) + 1
		isUpdate = true

		_, err = tx.Exec(
			`UPDATE submissions SET is_latest = 0 WHERE assignment_id = ? AND student_id = ?`,
			sub.AssignmentID, sub.StudentID,
		)
		if err != nil {
			return 0, false, fmt.Errorf("db: update previous submissions: %w", err)
		}
	}

	now := time.Now().UTC()
	if sub.CreatedAt.IsZero() {
		sub.CreatedAt = now
	}
	sub.Version = newVersion
	sub.IsLatest = true

	insertQuery := `INSERT INTO submissions (
		assignment_id, student_id, student_name, class_name, attachment_id,
		submitted_at, version, is_latest, is_late, target_filename, created_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

	res, err := tx.Exec(
		insertQuery,
		sub.AssignmentID,
		sub.StudentID,
		sub.StudentName,
		sub.ClassName,
		sub.AttachmentID,
		sub.SubmittedAt.UTC().Format(time.RFC3339),
		sub.Version,
		sub.IsLatest,
		sub.IsLate,
		sub.TargetFilename,
		sub.CreatedAt.UTC().Format(time.RFC3339),
	)
	if err != nil {
		return 0, false, fmt.Errorf("db: insert submission: %w", err)
	}

	id, err := res.LastInsertId()
	if err != nil {
		return 0, false, err
	}
	sub.ID = id

	if err := tx.Commit(); err != nil {
		return 0, false, fmt.Errorf("db: commit tx: %w", err)
	}

	return newVersion, isUpdate, nil
}

// [GetLatestSubmissions]
func (d *DB) GetLatestSubmissions(assignmentID string) ([]*Submission, error) {
	query := `SELECT s.id, s.assignment_id, s.student_id, s.student_name, s.class_name,
		s.attachment_id, s.submitted_at, s.version, s.is_latest, s.is_late, s.target_filename, s.created_at,
		a.file_size, a.sha256, a.storage_path
		FROM submissions s
		JOIN attachments a ON s.attachment_id = a.id
		WHERE s.assignment_id = ? AND s.is_latest = 1
		ORDER BY s.student_id ASC`

	rows, err := d.conn.Query(query, assignmentID)
	if err != nil {
		return nil, fmt.Errorf("db: query latest submissions: %w", err)
	}
	defer rows.Close()

	var list []*Submission
	for rows.Next() {
		var sub Submission
		var subAtStr, crAtStr string
		if err := rows.Scan(
			&sub.ID, &sub.AssignmentID, &sub.StudentID, &sub.StudentName, &sub.ClassName,
			&sub.AttachmentID, &subAtStr, &sub.Version, &sub.IsLatest, &sub.IsLate, &sub.TargetFilename, &crAtStr,
			&sub.FileSize, &sub.SHA256, &sub.StoragePath,
		); err != nil {
			return nil, fmt.Errorf("db: scan submission: %w", err)
		}
		sub.SubmittedAt, _ = time.Parse(time.RFC3339, subAtStr)
		sub.CreatedAt, _ = time.Parse(time.RFC3339, crAtStr)
		list = append(list, &sub)
	}
	return list, nil
}

// [GetStudentSubmissionHistory]
func (d *DB) GetStudentSubmissionHistory(assignmentID, studentID string) ([]*Submission, error) {
	query := `SELECT s.id, s.assignment_id, s.student_id, s.student_name, s.class_name,
		s.attachment_id, s.submitted_at, s.version, s.is_latest, s.is_late, s.target_filename, s.created_at,
		a.file_size, a.sha256, a.storage_path
		FROM submissions s
		JOIN attachments a ON s.attachment_id = a.id
		WHERE s.assignment_id = ? AND s.student_id = ?
		ORDER BY s.version DESC`

	rows, err := d.conn.Query(query, assignmentID, studentID)
	if err != nil {
		return nil, fmt.Errorf("db: query student history: %w", err)
	}
	defer rows.Close()

	var list []*Submission
	for rows.Next() {
		var sub Submission
		var subAtStr, crAtStr string
		if err := rows.Scan(
			&sub.ID, &sub.AssignmentID, &sub.StudentID, &sub.StudentName, &sub.ClassName,
			&sub.AttachmentID, &subAtStr, &sub.Version, &sub.IsLatest, &sub.IsLate, &sub.TargetFilename, &crAtStr,
			&sub.FileSize, &sub.SHA256, &sub.StoragePath,
		); err != nil {
			return nil, fmt.Errorf("db: scan history: %w", err)
		}
		sub.SubmittedAt, _ = time.Parse(time.RFC3339, subAtStr)
		sub.CreatedAt, _ = time.Parse(time.RFC3339, crAtStr)
		list = append(list, &sub)
	}
	return list, nil
}

// [GetLatestSubmissionForStudent]
func (d *DB) GetLatestSubmissionForStudent(assignmentID, studentID string) (*Submission, error) {
	query := `SELECT s.id, s.assignment_id, s.student_id, s.student_name, s.class_name,
		s.attachment_id, s.submitted_at, s.version, s.is_latest, s.is_late, s.target_filename, s.created_at,
		a.file_size, a.sha256, a.storage_path
		FROM submissions s
		JOIN attachments a ON s.attachment_id = a.id
		WHERE s.assignment_id = ? AND s.student_id = ? AND s.is_latest = 1
		LIMIT 1`

	var sub Submission
	var subAtStr, crAtStr string
	err := d.conn.QueryRow(query, assignmentID, studentID).Scan(
		&sub.ID, &sub.AssignmentID, &sub.StudentID, &sub.StudentName, &sub.ClassName,
		&sub.AttachmentID, &subAtStr, &sub.Version, &sub.IsLatest, &sub.IsLate, &sub.TargetFilename, &crAtStr,
		&sub.FileSize, &sub.SHA256, &sub.StoragePath,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("db: query student latest submission: %w", err)
	}
	sub.SubmittedAt, _ = time.Parse(time.RFC3339, subAtStr)
	sub.CreatedAt, _ = time.Parse(time.RFC3339, crAtStr)
	return &sub, nil
}

// [GetAllLatestSubmissions]
func (d *DB) GetAllLatestSubmissions() ([]*Submission, error) {
	query := `SELECT s.id, s.assignment_id, s.student_id, s.student_name, s.class_name,
		s.attachment_id, s.submitted_at, s.version, s.is_latest, s.is_late, s.target_filename, s.created_at,
		a.file_size, a.sha256, a.storage_path
		FROM submissions s
		JOIN attachments a ON s.attachment_id = a.id
		WHERE s.is_latest = 1
		ORDER BY s.assignment_id ASC, s.student_id ASC`

	rows, err := d.conn.Query(query)
	if err != nil {
		return nil, fmt.Errorf("db: query all latest submissions: %w", err)
	}
	defer rows.Close()

	var list []*Submission
	for rows.Next() {
		var sub Submission
		var subAtStr, crAtStr string
		if err := rows.Scan(
			&sub.ID, &sub.AssignmentID, &sub.StudentID, &sub.StudentName, &sub.ClassName,
			&sub.AttachmentID, &subAtStr, &sub.Version, &sub.IsLatest, &sub.IsLate, &sub.TargetFilename, &crAtStr,
			&sub.FileSize, &sub.SHA256, &sub.StoragePath,
		); err != nil {
			return nil, fmt.Errorf("db: scan all latest submissions: %w", err)
		}
		sub.SubmittedAt, _ = time.Parse(time.RFC3339, subAtStr)
		sub.CreatedAt, _ = time.Parse(time.RFC3339, crAtStr)
		list = append(list, &sub)
	}
	return list, nil
}

// [GetAllLatestSubmissionsForStudent]
func (d *DB) GetAllLatestSubmissionsForStudent(studentID string) ([]*Submission, error) {
	query := `SELECT s.id, s.assignment_id, s.student_id, s.student_name, s.class_name,
		s.attachment_id, s.submitted_at, s.version, s.is_latest, s.is_late, s.target_filename, s.created_at,
		a.file_size, a.sha256, a.storage_path
		FROM submissions s
		JOIN attachments a ON s.attachment_id = a.id
		WHERE s.student_id = ? AND s.is_latest = 1
		ORDER BY s.assignment_id ASC`

	rows, err := d.conn.Query(query, studentID)
	if err != nil {
		return nil, fmt.Errorf("db: query student all latest submissions: %w", err)
	}
	defer rows.Close()

	var list []*Submission
	for rows.Next() {
		var sub Submission
		var subAtStr, crAtStr string
		if err := rows.Scan(
			&sub.ID, &sub.AssignmentID, &sub.StudentID, &sub.StudentName, &sub.ClassName,
			&sub.AttachmentID, &subAtStr, &sub.Version, &sub.IsLatest, &sub.IsLate, &sub.TargetFilename, &crAtStr,
			&sub.FileSize, &sub.SHA256, &sub.StoragePath,
		); err != nil {
			return nil, fmt.Errorf("db: scan student submissions: %w", err)
		}
		sub.SubmittedAt, _ = time.Parse(time.RFC3339, subAtStr)
		sub.CreatedAt, _ = time.Parse(time.RFC3339, crAtStr)
		list = append(list, &sub)
	}
	return list, nil
}

// [StudentBindings]
func (d *DB) UpsertStudentBinding(b *StudentBinding) error {
	query := `INSERT INTO student_bindings (
		qq_id, student_id, student_name, class_name, created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?)
	ON CONFLICT(qq_id) DO UPDATE SET
		student_id = excluded.student_id,
		student_name = excluded.student_name,
		class_name = excluded.class_name,
		updated_at = excluded.updated_at`

	now := time.Now().UTC()
	if b.CreatedAt.IsZero() {
		b.CreatedAt = now
	}
	b.UpdatedAt = now

	_, err := d.conn.Exec(
		query,
		b.QQID,
		b.StudentID,
		b.StudentName,
		b.ClassName,
		b.CreatedAt.UTC().Format(time.RFC3339),
		b.UpdatedAt.UTC().Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("db: upsert student binding: %w", err)
	}
	return nil
}

func (d *DB) GetStudentBindingByQQ(qqID string) (*StudentBinding, error) {
	query := `SELECT qq_id, student_id, student_name, class_name, created_at, updated_at
		FROM student_bindings WHERE qq_id = ?`

	var b StudentBinding
	var crStr, upStr string
	err := d.conn.QueryRow(query, qqID).Scan(
		&b.QQID,
		&b.StudentID,
		&b.StudentName,
		&b.ClassName,
		&crStr,
		&upStr,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("db: get binding by qq: %w", err)
	}
	b.CreatedAt, _ = time.Parse(time.RFC3339, crStr)
	b.UpdatedAt, _ = time.Parse(time.RFC3339, upStr)
	return &b, nil
}

func (d *DB) GetStudentBindingByStudentID(studentID string) (*StudentBinding, error) {
	query := `SELECT qq_id, student_id, student_name, class_name, created_at, updated_at
		FROM student_bindings WHERE student_id = ? LIMIT 1`

	var b StudentBinding
	var crStr, upStr string
	err := d.conn.QueryRow(query, studentID).Scan(
		&b.QQID,
		&b.StudentID,
		&b.StudentName,
		&b.ClassName,
		&crStr,
		&upStr,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("db: get binding by student id: %w", err)
	}
	b.CreatedAt, _ = time.Parse(time.RFC3339, crStr)
	b.UpdatedAt, _ = time.Parse(time.RFC3339, upStr)
	return &b, nil
}

func (d *DB) DeleteStudentBinding(qqID string) error {
	query := `DELETE FROM student_bindings WHERE qq_id = ?`
	res, err := d.conn.Exec(query, qqID)
	if err != nil {
		return fmt.Errorf("db: delete student binding: %w", err)
	}
	rowsAffected, _ := res.RowsAffected()
	if rowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

func (d *DB) ListStudentBindings() ([]*StudentBinding, error) {
	query := `SELECT qq_id, student_id, student_name, class_name, created_at, updated_at
		FROM student_bindings ORDER BY student_id ASC`

	rows, err := d.conn.Query(query)
	if err != nil {
		return nil, fmt.Errorf("db: list student bindings: %w", err)
	}
	defer rows.Close()

	var list []*StudentBinding
	for rows.Next() {
		var b StudentBinding
		var crStr, upStr string
		if err := rows.Scan(
			&b.QQID,
			&b.StudentID,
			&b.StudentName,
			&b.ClassName,
			&crStr,
			&upStr,
		); err != nil {
			return nil, fmt.Errorf("db: scan student binding: %w", err)
		}
		b.CreatedAt, _ = time.Parse(time.RFC3339, crStr)
		b.UpdatedAt, _ = time.Parse(time.RFC3339, upStr)
		list = append(list, &b)
	}
	return list, nil
}

