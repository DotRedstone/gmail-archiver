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
	query := `SELECT 1 FROM attachments WHERE message_id = ? AND filename = ? LIMIT 1`
	var dummy int
	err := d.conn.QueryRow(query, messageID, filename).Scan(&dummy)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("db: exists query: %w", err)
	}
	return true, nil
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
