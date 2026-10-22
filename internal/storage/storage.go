package storage

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var (
	ErrPathTraversal = errors.New("storage: path traversal detected")
	invalidNameChars = regexp.MustCompile(`[^\w\.\-\p{Han}\p{Hiragana}\p{Katakana}\p{Hangul}]+`)
)

// [Engine]
type Engine struct {
	baseDir string
}

func New(dataDir string) (*Engine, error) {
	absBase, err := filepath.Abs(dataDir)
	if err != nil {
		return nil, fmt.Errorf("storage: resolve base dir: %w", err)
	}

	attachmentsDir := filepath.Join(absBase, "attachments")
	if err := os.MkdirAll(attachmentsDir, 0o755); err != nil {
		return nil, fmt.Errorf("storage: create attachments dir: %w", err)
	}

	return &Engine{baseDir: absBase}, nil
}

// [Sanitize]
func SanitizeFilename(name string) string {
	base := filepath.Base(name)
	base = strings.ReplaceAll(base, "\\", "_")
	base = strings.ReplaceAll(base, "/", "_")
	base = strings.TrimSpace(base)

	clean := invalidNameChars.ReplaceAllString(base, "_")
	clean = strings.Trim(clean, "._")
	if clean == "" {
		clean = "attachment.bin"
	}

	// Limit length to avoid filesystem issues
	if len(clean) > 180 {
		ext := filepath.Ext(clean)
		namePart := clean[:180-len(ext)]
		clean = namePart + ext
	}
	return clean
}

// [Writer]
func (e *Engine) Save(r io.Reader, filename string, receivedAt time.Time) (relPath string, hash string, size int64, err error) {
	cleanName := SanitizeFilename(filename)
	yearMonth := receivedAt.UTC().Format("2006/01")
	dirRel := filepath.Join("attachments", filepath.FromSlash(yearMonth))
	dirAbs := filepath.Join(e.baseDir, dirRel)

	if err := os.MkdirAll(dirAbs, 0o755); err != nil {
		return "", "", 0, fmt.Errorf("storage: mkdir: %w", err)
	}

	// Create temporary file
	randBuf := make([]byte, 8)
	_, _ = rand.Read(randBuf)
	tmpName := fmt.Sprintf(".tmp_%s_%x", cleanName, randBuf)
	tmpPath := filepath.Join(dirAbs, tmpName)

	tmpFile, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return "", "", 0, fmt.Errorf("storage: create tmp file: %w", err)
	}

	hasher := sha256.New()
	multiWriter := io.MultiWriter(tmpFile, hasher)

	written, copyErr := io.Copy(multiWriter, r)
	closeErr := tmpFile.Close()

	if copyErr != nil {
		_ = os.Remove(tmpPath)
		return "", "", 0, fmt.Errorf("storage: write tmp file: %w", copyErr)
	}
	if closeErr != nil {
		_ = os.Remove(tmpPath)
		return "", "", 0, fmt.Errorf("storage: close tmp file: %w", closeErr)
	}

	hashHex := hex.EncodeToString(hasher.Sum(nil))
	finalRel := filepath.Join(dirRel, fmt.Sprintf("%s_%s", hashHex, cleanName))
	finalAbs := filepath.Join(e.baseDir, finalRel)

	// Check if identical target file already exists
	if fi, err := os.Stat(finalAbs); err == nil && fi.Size() == written {
		_ = os.Remove(tmpPath)
		return filepath.ToSlash(finalRel), hashHex, written, nil
	}

	if err := os.Rename(tmpPath, finalAbs); err != nil {
		_ = os.Remove(tmpPath)
		return "", "", 0, fmt.Errorf("storage: rename: %w", err)
	}

	return filepath.ToSlash(finalRel), hashHex, written, nil
}

// [Security]
func (e *Engine) ResolveAbsolutePath(relPath string) (string, error) {
	cleanRel := filepath.Clean(filepath.FromSlash(relPath))
	if strings.HasPrefix(cleanRel, "..") || filepath.IsAbs(cleanRel) {
		return "", ErrPathTraversal
	}

	absPath := filepath.Join(e.baseDir, cleanRel)
	rel, err := filepath.Rel(e.baseDir, absPath)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", ErrPathTraversal
	}

	return absPath, nil
}
