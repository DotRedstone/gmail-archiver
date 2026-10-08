package parser

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/http/cookiejar"
	"net/mail"
	"net/url"
	"path/filepath"
	"regexp"
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
	storage    *storage.Engine
	decoder    *mime.WordDecoder
	httpClient *http.Client
}

func New(storageEngine *storage.Engine) *Parser {
	jar, _ := cookiejar.New(nil)
	return &Parser{
		storage: storageEngine,
		decoder: &mime.WordDecoder{},
		httpClient: &http.Client{
			Jar:     jar,
			Timeout: 60 * time.Second,
		},
	}
}

func (p *Parser) SetHTTPClient(client *http.Client) {
	p.httpClient = client
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
	var inlineBodies []string
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
			} else {
				bodyBuf, _ := io.ReadAll(io.LimitReader(part.Body, 512*1024))
				bodyStr := string(bodyBuf)
				if isPlainBodyType(contentType) {
					inlineBodies = append(inlineBodies, bodyStr)
					if strings.EqualFold(contentType, "text/plain") && meta.BodyText == "" {
						meta.BodyText = bodyStr
					}
				}
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

	if meta.BodyText == "" && len(inlineBodies) > 0 {
		meta.BodyText = inlineBodies[0]
	}

	// Detect and download QQ Mail large attachments from inline bodies
	p.extractQQBigAttachments(inlineBodies, meta)

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

// [QQBigAttachment]
var qqLinkRe = regexp.MustCompile(`https?://[^/\s"'>]+/ftn/download\?[^\s"'<>]+`)

type qqFTNResp struct {
	Head struct {
		Ret int    `json:"ret"`
		Msg string `json:"msg"`
	} `json:"head"`
	Body struct {
		Name string `json:"name"`
		Url  string `json:"url"`
		Size int64  `json:"size"`
	} `json:"body"`
}

func (p *Parser) extractQQBigAttachments(bodies []string, meta *EmailMetadata) {
	if p.httpClient == nil {
		return
	}
	seenLinks := make(map[string]bool)
	for _, body := range bodies {
		links := qqLinkRe.FindAllString(body, -1)
		for _, rawLink := range links {
			cleanLink := strings.ReplaceAll(rawLink, "&amp;", "&")
			if seenLinks[cleanLink] {
				continue
			}
			seenLinks[cleanLink] = true

			u, err := url.Parse(cleanLink)
			if err != nil {
				continue
			}
			if !strings.Contains(u.Host, "qq.com") && !strings.Contains(u.Host, "127.0.0.1") && !strings.Contains(u.Host, "localhost") {
				continue
			}
			q := u.Query()
			key := q.Get("key")
			code := q.Get("code")
			k := q.Get("k")

			form := url.Values{"f": {"json"}}
			if key != "" && code != "" {
				form.Set("func", "3")
				form.Set("key", key)
				form.Set("code", code)
			} else if k != "" {
				form.Set("k", k)
			} else {
				continue
			}

			apiURL := "https://wx.mail.qq.com/ftn/download"
			if u.Host != "wx.mail.qq.com" && u.Host != "" {
				apiURL = fmt.Sprintf("%s://%s/ftn/download", u.Scheme, u.Host)
			}

			req, err := http.NewRequest("POST", apiURL, strings.NewReader(form.Encode()))
			if err != nil {
				continue
			}
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.Header.Set("User-Agent", "Mozilla/5.0")

			resp, err := p.httpClient.Do(req)
			if err != nil {
				continue
			}

			var ftnResp qqFTNResp
			decodeErr := json.NewDecoder(resp.Body).Decode(&ftnResp)
			resp.Body.Close()
			if decodeErr != nil || ftnResp.Head.Ret != 0 || ftnResp.Body.Url == "" {
				continue
			}

			filename := ftnResp.Body.Name
			if filename == "" {
				filename = "qq_big_attachment.zip"
			}

			downURL := ftnResp.Body.Url
			downReq, err := http.NewRequest("GET", downURL, nil)
			if err != nil {
				continue
			}
			downReq.Header.Set("User-Agent", "Mozilla/5.0")

			downResp, err := p.httpClient.Do(downReq)
			if err != nil {
				continue
			}
			if downResp.StatusCode >= 400 {
				downResp.Body.Close()
				continue
			}

			relPath, sha256Hex, size, err := p.storage.Save(downResp.Body, filename, meta.ReceivedAt)
			downResp.Body.Close()
			if err != nil {
				continue
			}

			cleanFilename := storage.SanitizeFilename(filename)
			mimeType := mime.TypeByExtension(filepath.Ext(cleanFilename))
			if mimeType == "" {
				mimeType = "application/zip"
			}

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
	}
}
