package db

import (
	"errors"
	"sync"
	"testing"
	"time"
)

func TestExternalBindingIntentIsolatedAndUnique(t *testing.T) {
	database, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	now := time.Now().UTC()
	base := BindingIntent{
		ExternalBinding: ExternalBinding{PlatformID: "course-qq", BotID: "bot-example", StudentID: "990000000001", StudentName: "示例甲", ClassName: "示例班A"},
		ExpiresAt:       now.Add(time.Minute),
	}
	first, second := base, base
	first.ExternalUserID = "user-one"
	second.ExternalUserID = "user-two"
	for _, intent := range []BindingIntent{first, second} {
		if err := database.CreateBindingIntent(intent); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, userID := range []string{"user-one", "user-two"} {
		wg.Add(1)
		go func(userID string) {
			defer wg.Done()
			_, err := database.ConfirmBindingIntent(base.PlatformID, base.BotID, userID, now)
			results <- err
		}(userID)
	}
	wg.Wait()
	close(results)
	success, conflict := 0, 0
	for err := range results {
		switch {
		case err == nil:
			success++
		case errors.Is(err, ErrBindingConflict):
			conflict++
		default:
			t.Fatalf("unexpected confirm result: %v", err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("want one winner and one conflict, got %d and %d", success, conflict)
	}
	bindings, err := database.ListExternalBindings()
	if err != nil || len(bindings) != 1 || bindings[0].Source != "self_asserted" {
		t.Fatalf("unexpected bindings: count=%d err=%v", len(bindings), err)
	}
	if _, err := database.GetStudentBindingByStudentID(base.StudentID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("legacy table must not inherit OpenID: %v", err)
	}
}

func TestLegacyBindingBlocksUnreviewedNewClaim(t *testing.T) {
	database, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := database.UpsertStudentBinding(&StudentBinding{QQID: "9000000001", StudentID: "990000000001", StudentName: "示例甲"}); err != nil {
		t.Fatal(err)
	}
	intent := BindingIntent{ExternalBinding: ExternalBinding{
		PlatformID: "course-qq", BotID: "bot-example", ExternalUserID: "user-one",
		StudentID: "990000000001", StudentName: "示例甲",
	}, ExpiresAt: time.Now().Add(time.Minute)}
	if err := database.CreateBindingIntent(intent); !errors.Is(err, ErrBindingConflict) {
		t.Fatalf("legacy identity should require review, got %v", err)
	}
}

func TestExpiredExternalBindingIntentCannotConfirm(t *testing.T) {
	database, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	intent := BindingIntent{ExternalBinding: ExternalBinding{
		PlatformID: "course-qq", BotID: "bot-example", ExternalUserID: "user-one",
		StudentID: "990000000001", StudentName: "示例甲",
	}, ExpiresAt: time.Now().Add(-time.Second)}
	if err := database.CreateBindingIntent(intent); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ConfirmBindingIntent(intent.PlatformID, intent.BotID, intent.ExternalUserID, time.Now()); !errors.Is(err, ErrIntentExpired) {
		t.Fatalf("expired claim accepted: %v", err)
	}
	if _, err := database.GetBindingIntent(intent.PlatformID, intent.BotID, intent.ExternalUserID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired claim remained available: %v", err)
	}
}
