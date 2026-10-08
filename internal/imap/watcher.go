package imap

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"mime"
	"sort"
	"sync"
	"time"

	"github.com/emersion/go-message/charset"

	"github.com/dot/gmail-archiver/internal/config"
	"github.com/dot/gmail-archiver/internal/db"
	"github.com/dot/gmail-archiver/internal/parser"
	"github.com/dot/gmail-archiver/internal/rule"
	goimap "github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// [Status]
type Status struct {
	Connected      bool      `json:"connected"`
	State          string    `json:"state"` // "disconnected", "connected", "syncing", "idle"
	Mailbox        string    `json:"mailbox"`
	LastSyncTime   time.Time `json:"last_sync_time"`
	LastError      string    `json:"last_error,omitempty"`
	ProcessedCount int64     `json:"processed_count"`
}

// [Watcher]
type Watcher struct {
	cfg    *config.Config
	db     *db.DB
	parser *parser.Parser
	rules  *rule.Engine

	mu     sync.RWMutex
	status Status
}

func NewWatcher(cfg *config.Config, database *db.DB, emailParser *parser.Parser, ruleEngine *rule.Engine) *Watcher {
	return &Watcher{
		cfg:    cfg,
		db:     database,
		parser: emailParser,
		rules:  ruleEngine,
		status: Status{
			State:   "disconnected",
			Mailbox: "INBOX",
		},
	}
}

func (w *Watcher) GetStatus() Status {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.status
}

func (w *Watcher) updateStatus(fn func(s *Status)) {
	w.mu.Lock()
	defer w.mu.Unlock()
	fn(&w.status)
}

// [Lifecycle]
func (w *Watcher) Start(ctx context.Context) {
	backoff := 1 * time.Second
	maxBackoff := 60 * time.Second

	for {
		select {
		case <-ctx.Done():
			log.Println("[imap] watcher loop exiting by context cancel")
			w.updateStatus(func(s *Status) {
				s.Connected = false
				s.State = "disconnected"
			})
			return
		default:
		}

		log.Printf("[imap] connecting to %s ...", w.cfg.IMAPServer)
		err := w.runSession(ctx)
		if err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("[imap] session error: %v", err)
			w.updateStatus(func(s *Status) {
				s.Connected = false
				s.State = "disconnected"
				s.LastError = err.Error()
			})
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
		}
	}
}

// [Session]
func (w *Watcher) runSession(ctx context.Context) error {
	notifyCh := make(chan struct{}, 1)

	options := &imapclient.Options{
		WordDecoder: &mime.WordDecoder{CharsetReader: charset.Reader},
		UnilateralDataHandler: &imapclient.UnilateralDataHandler{
			Mailbox: func(data *imapclient.UnilateralDataMailbox) {
				if data.NumMessages != nil {
					select {
					case notifyCh <- struct{}{}:
					default:
					}
				}
			},
		},
	}

	client, err := imapclient.DialTLS(w.cfg.IMAPServer, options)
	if err != nil {
		return fmt.Errorf("dial tls: %w", err)
	}
	defer client.Close()

	if err := client.Login(w.cfg.IMAPUser, w.cfg.IMAPPassword).Wait(); err != nil {
		return fmt.Errorf("login: %w", err)
	}
	log.Printf("[imap] successfully logged in as %s", w.cfg.IMAPUser)

	mailbox := "INBOX"
	if _, err := client.Select(mailbox, nil).Wait(); err != nil {
		return fmt.Errorf("select mailbox %s: %w", mailbox, err)
	}

	w.updateStatus(func(s *Status) {
		s.Connected = true
		s.State = "connected"
		s.Mailbox = mailbox
		s.LastError = ""
	})

	// Initial sync
	if err := w.syncNewMessages(ctx, client, mailbox); err != nil {
		return fmt.Errorf("initial sync: %w", err)
	}

	refreshTicker := time.NewTicker(w.cfg.IdleRefreshInterval)
	defer refreshTicker.Stop()

	// IDLE Loop
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		w.updateStatus(func(s *Status) {
			s.State = "idle"
		})
		log.Println("[imap] entering IDLE mode ...")

		idleCmd, err := client.Idle()
		if err != nil {
			return fmt.Errorf("idle command: %w", err)
		}

		idleExited := false
		select {
		case <-ctx.Done():
			_ = idleCmd.Close()
			_ = idleCmd.Wait()
			return nil

		case <-notifyCh:
			log.Println("[imap] received mailbox update notification, exiting IDLE")
			if err := idleCmd.Close(); err != nil {
				return fmt.Errorf("close idle on notify: %w", err)
			}
			if err := idleCmd.Wait(); err != nil {
				return fmt.Errorf("wait idle on notify: %w", err)
			}
			idleExited = true

		case <-refreshTicker.C:
			log.Println("[imap] refreshing IDLE connection")
			if err := idleCmd.Close(); err != nil {
				return fmt.Errorf("close idle on refresh: %w", err)
			}
			if err := idleCmd.Wait(); err != nil {
				return fmt.Errorf("wait idle on refresh: %w", err)
			}
			idleExited = true
		}

		if idleExited {
			if err := w.syncNewMessages(ctx, client, mailbox); err != nil {
				return fmt.Errorf("sync messages: %w", err)
			}
		}
	}
}

// [Sync]
func (w *Watcher) syncNewMessages(ctx context.Context, client *imapclient.Client, mailbox string) error {
	w.updateStatus(func(s *Status) {
		s.State = "syncing"
	})

	lastUID, err := w.db.GetLastSyncedUID(mailbox)
	if err != nil {
		return fmt.Errorf("get last synced uid: %w", err)
	}

	criteria := &goimap.SearchCriteria{}
	if lastUID > 0 {
		var uidSet goimap.UIDSet
		uidSet.AddRange(goimap.UID(lastUID+1), math.MaxUint32)
		criteria.UID = []goimap.UIDSet{uidSet}
	}

	searchCmd := client.UIDSearch(criteria, nil)
	searchData, err := searchCmd.Wait()
	if err != nil {
		return fmt.Errorf("uid search: %w", err)
	}

	uids := searchData.AllUIDs()
	if len(uids) == 0 {
		w.updateStatus(func(s *Status) {
			s.LastSyncTime = time.Now().UTC()
		})
		return nil
	}

	// Sort UIDs ascending
	sort.Slice(uids, func(i, j int) bool {
		return uids[i] < uids[j]
	})

	log.Printf("[imap] found %d message(s) to inspect (since UID %d)", len(uids), lastUID)

	// Filter pending UIDs
	var pending []goimap.UID
	for _, uid := range uids {
		if uint32(uid) > lastUID {
			pending = append(pending, uid)
		}
	}
	if len(pending) == 0 {
		w.updateStatus(func(s *Status) {
			s.LastSyncTime = time.Now().UTC()
		})
		return nil
	}

	log.Printf("[imap] inspecting %d new message(s) in batches", len(pending))

	chunkSize := 25
	for i := 0; i < len(pending); i += chunkSize {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		end := i + chunkSize
		if end > len(pending) {
			end = len(pending)
		}
		chunk := pending[i:end]

		var uidSet goimap.UIDSet
		uidSet.AddNum(chunk...)

		fetchCmd := client.Fetch(uidSet, &goimap.FetchOptions{
			UID:         true,
			BodySection: []*goimap.FetchItemBodySection{{}},
		})
		buffers, err := fetchCmd.Collect()
		if err != nil {
			log.Printf("[imap] batch fetch error: %v", err)
			continue
		}

		for _, msgBuf := range buffers {
			bodyBytes := msgBuf.FindBodySection(&goimap.FetchItemBodySection{})
			if len(bodyBytes) > 0 {
				if err := w.processMessageBody(msgBuf.UID, bodyBytes); err != nil {
					log.Printf("[imap] error processing message UID %d: %v", msgBuf.UID, err)
				}
			}

			if err := w.db.SetLastSyncedUID(mailbox, uint32(msgBuf.UID)); err != nil {
				log.Printf("[imap] failed to record synced UID %d: %v", msgBuf.UID, err)
			}
			if uint32(msgBuf.UID) > lastUID {
				lastUID = uint32(msgBuf.UID)
			}
		}
	}

	w.updateStatus(func(s *Status) {
		s.LastSyncTime = time.Now().UTC()
	})
	return nil
}

// [Archive]
func (w *Watcher) processMessageBody(uid goimap.UID, bodyBytes []byte) error {
	meta, err := w.parser.Parse(bytes.NewReader(bodyBytes))
	if err != nil {
		return fmt.Errorf("parse email: %w", err)
	}

	if len(meta.Attachments) == 0 {
		return nil
	}

	log.Printf("[imap] message UID %d (%q) has %d attachment(s)", uid, meta.Subject, len(meta.Attachments))

	savedCount := 0
	for _, att := range meta.Attachments {
		var id int64
		existingID, exists, err := w.db.GetAttachmentIDByMessageAndFilename(att.MessageID, att.Filename)
		if err != nil {
			log.Printf("[imap] check attachment existence error: %v", err)
		}
		if exists {
			id = existingID
		} else {
			newID, err := w.db.InsertAttachment(att)
			if err != nil {
				log.Printf("[imap] save attachment to db error: %v", err)
				continue
			}
			id = newID
			savedCount++
			log.Printf("[imap] archived attachment id=%d filename=%q size=%d bytes", id, att.Filename, att.FileSize)
		}

		// Assignment rule matching & overwrite update mechanism
		if w.rules != nil {
			if _, matchRes, ok := w.rules.Match(meta.Subject, att.Filename, meta.BodyText, meta.ReceivedAt); ok {
				sub := &db.Submission{
					AssignmentID:   matchRes.AssignmentID,
					StudentID:      matchRes.StudentID,
					StudentName:    matchRes.StudentName,
					ClassName:      matchRes.ClassName,
					AttachmentID:   id,
					SubmittedAt:    meta.ReceivedAt,
					IsLate:         matchRes.IsLate,
					TargetFilename: matchRes.TargetFilename,
				}
				ver, isUpdate, subErr := w.db.RecordSubmission(sub)
				if subErr != nil {
					log.Printf("[imap] record submission error for student %s: %v", matchRes.StudentID, subErr)
				} else {
					log.Printf("[imap] assignment %s recorded: student %s (%s) v%d [update=%v, late=%v]",
						matchRes.AssignmentID, matchRes.StudentID, matchRes.StudentName, ver, isUpdate, matchRes.IsLate)
				}
			}
		}
	}

	w.updateStatus(func(s *Status) {
		s.ProcessedCount += int64(savedCount)
	})

	return nil
}
