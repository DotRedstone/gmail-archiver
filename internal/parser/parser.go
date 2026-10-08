package parser

import (
	"errors"
	"fmt"
	"io"
	"mime"
	"net/mail"
	"path/filepath"
	"strings"
	"time"

	"github.com/emersion/go-message"
	_ "github.com/emersion/go-message/charset"
	gomail "github.com/emersion/go-message/mail"

	"github.com/dot/gmail-archiver/internal/db"
	"github.com/dot/gmail-archiver/internal/storage"
)

// [Parser]
type Parser struct {
	storage *storage.Engine
	decoder *mime.WordDecoder
}

func New(storageEngine *storage.Engine) *Parser {
	return &Parser{
		storage: storageEngine,
		decoder: &mime.WordDecoder{},
	}
}

// [EmailMetadata]
type EmailMetadata struct {
	MessageID   string
	Sender      string
	Subject     string
	ReceivedAt  time.Time
	BodyText    string
	Attachments []*db.Attachment
}

// [Parse]
func (p *Parser) Parse(r io.Reader) (*EmailMetadata, error) {
	mr, err := gomail.CreateReader(r)
	if err != nil {
		return nil, fmt.Errorf("parser: create mail reader: %w", err)
	}
	defer mr.Close()

	meta := &EmailMetadata{
		ReceivedAt: time.Now().UTC(),
	}

	// Extract Message-ID
	if msgID, err := mr.Header.MessageID(); err == nil && msgID != "" {
		meta.MessageID = msgID
	} else if rawID := mr.Header.Get("Message-Id"); rawID != "" {
		meta.MessageID = strings.TrimSpace(rawID)
	}
	if meta.MessageID == "" {
		meta.MessageID = fmt.Sprintf("<generated-%d@gmail-archiver>", time.Now().UnixNano())
	} else if !strings.HasPrefix(meta.MessageID, "<") {
		meta.MessageID = "<" + meta.MessageID + ">"
	}

	// Extract Subject
	if subj, err := mr.Header.Subject(); err == nil {
		meta.Subject = subj
	} else if rawSubj := mr.Header.Get("Subject"); rawSubj != "" {
		meta.Subject = p.decodeHeader(rawSubj)
	}

	// Extract Date
	if date, err := mr.Header.Date(); err == nil && !date.IsZero() {
		meta.ReceivedAt = date.UTC()
	} else if rawDate := mr.Header.Get("Date"); rawDate != "" {
		if t, err := mail.ParseDate(rawDate); err == nil {
			meta.ReceivedAt = t.UTC()
		}
	}

	// Extract From / Sender
	if fromList, err := mr.Header.AddressList("From"); err == nil && len(fromList) > 0 {
		meta.Sender = fromList[0].String()
	} else if rawFrom := mr.Header.Get("From"); rawFrom != "" {
		meta.Sender = p.decodeHeader(rawFrom)
	}

	// Extract attachments
	partIndex := 0
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			break
		}
		partIndex++

		var filename, mimeType string
		var isAttachment bool

		switch h := part.Header.(type) {
		case *gomail.AttachmentHeader:
			isAttachment = true
			mimeType, _, _ = h.ContentType()
			filename, _ = h.Filename()
			if filename == "" {
				filename = p.extractFilenameFromHeader(h.Header)
			}
		case *gomail.InlineHeader:
			contentType, _, _ := h.ContentType()
			fn := p.extractFilenameFromHeader(h.Header)
			// If an inline part explicitly specifies a filename and is not plain text/html, archive it
			if fn != "" && !isPlainBodyType(contentType) {
				isAttachment = true
				mimeType = contentType
				filename = fn
			} else if strings.EqualFold(contentType, "text/plain") && meta.BodyText == "" {
				bodyBuf, _ := io.ReadAll(io.LimitReader(part.Body, 32*1024))
				meta.BodyText = string(bodyBuf)
			}
		}

		if !isAttachment {
			continue
		}

		if filename == "" {
			ext := p.suggestExtension(mimeType)
			filename = fmt.Sprintf("attachment_%d%s", partIndex, ext)
		} else {
			filename = p.decodeHeader(filename)
		}

		relPath, sha256Hex, size, err := p.storage.Save(part.Body, filename, meta.ReceivedAt)
		if err != nil {
			return nil, fmt.Errorf("parser: save attachment %q: %w", filename, err)
		}

		cleanFilename := storage.SanitizeFilename(filename)
		att := &db.Attachment{
			MessageID:   meta.MessageID,
			Sender:      meta.Sender,
			Subject:     meta.Subject,
			ReceivedAt:  meta.ReceivedAt,
			Filename:    cleanFilename,
			FileSize:    size,
			SHA256:      sha256Hex,
			MIMEType:    mimeType,
			StoragePath: relPath,
			CreatedAt:   time.Now().UTC(),
		}

		meta.Attachments = append(meta.Attachments, att)
	}

	return meta, nil
}

// [Helpers]
func (p *Parser) decodeHeader(input string) string {
	decoded, err := p.decoder.DecodeHeader(input)
	if err == nil {
		return decoded
	}
	return input
}

func (p *Parser) extractFilenameFromHeader(h message.Header) string {
	// Check Content-Disposition
	if disp := h.Get("Content-Disposition"); disp != "" {
		if _, params, err := mime.ParseMediaType(disp); err == nil {
			if fn, ok := params["filename"]; ok && fn != "" {
				return fn
			}
		}
	}
	// Check Content-Type name param
	if ct := h.Get("Content-Type"); ct != "" {
		if _, params, err := mime.ParseMediaType(ct); err == nil {
			if name, ok := params["name"]; ok && name != "" {
				return name
			}
		}
	}
	return ""
}

func isPlainBodyType(contentType string) bool {
	ct := strings.ToLower(strings.TrimSpace(contentType))
	return ct == "text/plain" || ct == "text/html"
}

func (p *Parser) suggestExtension(mimeType string) string {
	switch strings.ToLower(mimeType) {
	case "image/png":
		return ".png"
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "application/pdf":
		return ".pdf"
	case "application/zip":
		return ".zip"
	case "application/json":
		return ".json"
	default:
		exts, err := mime.ExtensionsByType(mimeType)
		if err == nil && len(exts) > 0 {
			return exts[0]
		}
		return filepath.Ext("")
	}
}
