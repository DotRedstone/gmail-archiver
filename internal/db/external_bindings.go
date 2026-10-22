package db

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

var (
	ErrBindingConflict = errors.New("db: binding already occupied")
	ErrIntentExpired   = errors.New("db: binding intent expired")
)

// ExternalBinding uses the platform's opaque identity. Legacy qq_id rows remain
// separate because a numeric QQ account cannot be interpreted as an OpenID.
type ExternalBinding struct {
	PlatformID     string     `json:"platform_id"`
	BotID          string     `json:"bot_id"`
	ExternalUserID string     `json:"external_user_id"`
	StudentID      string     `json:"student_id"`
	StudentName    string     `json:"student_name"`
	ClassName      string     `json:"class_name"`
	Source         string     `json:"source"`
	VerifiedBy     string     `json:"verified_by,omitempty"`
	VerifiedAt     *time.Time `json:"verified_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
}

type BindingIntent struct {
	ExternalBinding
	ExpiresAt time.Time `json:"expires_at"`
}

func scanExternalBinding(row interface{ Scan(...any) error }) (*ExternalBinding, error) {
	var b ExternalBinding
	var created string
	var verifiedBy, verifiedAt sql.NullString
	err := row.Scan(&b.PlatformID, &b.BotID, &b.ExternalUserID, &b.StudentID, &b.StudentName, &b.ClassName, &b.Source, &verifiedBy, &verifiedAt, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	b.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	if verifiedBy.Valid {
		b.VerifiedBy = verifiedBy.String
	}
	if verifiedAt.Valid {
		t, parseErr := time.Parse(time.RFC3339Nano, verifiedAt.String)
		if parseErr != nil {
			return nil, parseErr
		}
		b.VerifiedAt = &t
	}
	return &b, err
}

const externalBindingColumns = `platform_id, bot_id, external_user_id, student_id, student_name, class_name, source, verified_by, verified_at, created_at`

func (d *DB) GetExternalBinding(platformID, botID, userID string) (*ExternalBinding, error) {
	return scanExternalBinding(d.conn.QueryRow(`SELECT `+externalBindingColumns+` FROM external_bindings WHERE platform_id=? AND bot_id=? AND external_user_id=?`, platformID, botID, userID))
}

func (d *DB) GetExternalBindingByStudent(studentID string) (*ExternalBinding, error) {
	return scanExternalBinding(d.conn.QueryRow(`SELECT `+externalBindingColumns+` FROM external_bindings WHERE student_id=?`, studentID))
}

func (d *DB) ListExternalBindings() ([]*ExternalBinding, error) {
	rows, err := d.conn.Query(`SELECT ` + externalBindingColumns + ` FROM external_bindings ORDER BY student_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]*ExternalBinding, 0)
	for rows.Next() {
		b, err := scanExternalBinding(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, b)
	}
	return result, rows.Err()
}

func (d *DB) ListExternalBindingsForBot(platformID, botID string) ([]*ExternalBinding, error) {
	rows, err := d.conn.Query(`SELECT `+externalBindingColumns+` FROM external_bindings
		WHERE platform_id=? AND bot_id=? ORDER BY student_id`, platformID, botID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]*ExternalBinding, 0)
	for rows.Next() {
		binding, err := scanExternalBinding(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, binding)
	}
	return result, rows.Err()
}

func (d *DB) CreateBindingIntent(intent BindingIntent) error {
	tx, err := d.conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var occupied int
	err = tx.QueryRow(`SELECT 1 FROM external_bindings WHERE student_id=? OR (platform_id=? AND bot_id=? AND external_user_id=?) LIMIT 1`,
		intent.StudentID, intent.PlatformID, intent.BotID, intent.ExternalUserID).Scan(&occupied)
	if err == nil {
		return ErrBindingConflict
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	// Old QQ-number bindings are not OpenIDs, but an occupied student still needs
	// review before the new platform can claim that identity.
	err = tx.QueryRow(`SELECT 1 FROM student_bindings WHERE student_id=? LIMIT 1`, intent.StudentID).Scan(&occupied)
	if err == nil {
		return ErrBindingConflict
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	_, err = tx.Exec(`INSERT INTO binding_intents (platform_id, bot_id, external_user_id, student_id, student_name, class_name, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(platform_id, bot_id, external_user_id) DO UPDATE SET student_id=excluded.student_id,
		student_name=excluded.student_name, class_name=excluded.class_name, expires_at=excluded.expires_at`,
		intent.PlatformID, intent.BotID, intent.ExternalUserID, intent.StudentID, intent.StudentName, intent.ClassName,
		intent.ExpiresAt.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (d *DB) GetBindingIntent(platformID, botID, userID string) (*BindingIntent, error) {
	var intent BindingIntent
	var expires string
	err := d.conn.QueryRow(`SELECT platform_id, bot_id, external_user_id, student_id, student_name, class_name, expires_at
		FROM binding_intents WHERE platform_id=? AND bot_id=? AND external_user_id=?`, platformID, botID, userID).
		Scan(&intent.PlatformID, &intent.BotID, &intent.ExternalUserID, &intent.StudentID, &intent.StudentName, &intent.ClassName, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	intent.ExpiresAt, err = time.Parse(time.RFC3339Nano, expires)
	return &intent, err
}

func (d *DB) CancelBindingIntent(platformID, botID, userID string) error {
	_, err := d.conn.Exec(`DELETE FROM binding_intents WHERE platform_id=? AND bot_id=? AND external_user_id=?`, platformID, botID, userID)
	return err
}

// Expiry and roster-drift cleanup must not delete a newer five-minute intent.
func (d *DB) CancelBindingIntentIfMatches(intent BindingIntent) error {
	_, err := d.conn.Exec(`DELETE FROM binding_intents WHERE platform_id=? AND bot_id=? AND external_user_id=?
		AND student_id=? AND student_name=? AND class_name=? AND expires_at=?`,
		intent.PlatformID, intent.BotID, intent.ExternalUserID, intent.StudentID,
		intent.StudentName, intent.ClassName, intent.ExpiresAt.UTC().Format(time.RFC3339Nano))
	return err
}

func (d *DB) ConfirmBindingIntent(platformID, botID, userID string, now time.Time) (*ExternalBinding, error) {
	intent, err := d.GetBindingIntent(platformID, botID, userID)
	if err != nil {
		return nil, err
	}
	return d.ConfirmMatchingBindingIntent(*intent, now)
}

// ConfirmMatchingBindingIntent atomically consumes only the intent previously
// checked against the roster. A concurrent replacement must not bind an
// unchecked student under the same external identity.
func (d *DB) ConfirmMatchingBindingIntent(intent BindingIntent, now time.Time) (*ExternalBinding, error) {
	platformID, botID, userID := intent.PlatformID, intent.BotID, intent.ExternalUserID
	tx, err := d.conn.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var b ExternalBinding
	var expires string
	err = tx.QueryRow(`SELECT student_id, student_name, class_name, expires_at FROM binding_intents
		WHERE platform_id=? AND bot_id=? AND external_user_id=?`, platformID, botID, userID).
		Scan(&b.StudentID, &b.StudentName, &b.ClassName, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	expiration, err := time.Parse(time.RFC3339Nano, expires)
	if err != nil {
		return nil, err
	}
	if b.StudentID != intent.StudentID || b.StudentName != intent.StudentName ||
		b.ClassName != intent.ClassName || !expiration.Equal(intent.ExpiresAt) {
		return nil, ErrBindingConflict
	}
	if _, err = tx.Exec(`DELETE FROM binding_intents WHERE platform_id=? AND bot_id=? AND external_user_id=?`, platformID, botID, userID); err != nil {
		return nil, err
	}
	expiration, err = time.Parse(time.RFC3339Nano, expires)
	if err != nil {
		return nil, err
	}
	if !now.Before(expiration) {
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return nil, ErrIntentExpired
	}
	var legacyOccupied int
	err = tx.QueryRow(`SELECT 1 FROM student_bindings WHERE student_id=? LIMIT 1`, b.StudentID).Scan(&legacyOccupied)
	if err == nil {
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return nil, ErrBindingConflict
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	var occupied int
	err = tx.QueryRow(`SELECT 1 FROM external_bindings WHERE student_id=? OR (platform_id=? AND bot_id=? AND external_user_id=?) LIMIT 1`,
		b.StudentID, platformID, botID, userID).Scan(&occupied)
	if err == nil {
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return nil, ErrBindingConflict
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	b.PlatformID, b.BotID, b.ExternalUserID = platformID, botID, userID
	b.Source = "self_asserted"
	b.CreatedAt = now.UTC()
	_, err = tx.Exec(`INSERT INTO external_bindings (platform_id, bot_id, external_user_id, student_id, student_name, class_name, source, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		b.PlatformID, b.BotID, b.ExternalUserID, b.StudentID, b.StudentName, b.ClassName, b.Source,
		b.CreatedAt.Format(time.RFC3339Nano))
	if err != nil {
		// The database's primary key and unique student key settle concurrent claims.
		return nil, fmt.Errorf("db: insert external binding: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("db: confirm binding: %w", err)
	}
	return &b, nil
}

func (d *DB) DeleteExternalBinding(studentID string) error {
	result, err := d.conn.Exec(`DELETE FROM external_bindings WHERE student_id=?`, studentID)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (d *DB) VerifyExternalBinding(studentID, platformID, botID, userID, verifier string, now time.Time) (*ExternalBinding, error) {
	result, err := d.conn.Exec(`UPDATE external_bindings SET source='staff_verified', verified_by=?, verified_at=?
		WHERE student_id=? AND platform_id=? AND bot_id=? AND external_user_id=?`,
		verifier, now.UTC().Format(time.RFC3339Nano), studentID, platformID, botID, userID)
	if err != nil {
		return nil, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, ErrNotFound
	}
	return d.GetExternalBindingByStudent(studentID)
}
