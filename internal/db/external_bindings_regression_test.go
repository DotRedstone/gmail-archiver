package db

import (
	"errors"
	"testing"
	"time"
)

func TestBindingIntentReplacementCannotConsumeUnverifiedStudent(t *testing.T) {
	database, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	now := time.Now().UTC()
	intent := BindingIntent{ExternalBinding: ExternalBinding{
		PlatformID: "course-qq", BotID: "bot-example", ExternalUserID: "user-one",
		StudentID: "990000000001", StudentName: "示例甲", ClassName: "示例班A",
	}, ExpiresAt: now.Add(5 * time.Minute)}
	if err := database.CreateBindingIntent(intent); err != nil {
		t.Fatal(err)
	}
	checked, err := database.GetBindingIntent(intent.PlatformID, intent.BotID, intent.ExternalUserID)
	if err != nil {
		t.Fatal(err)
	}
	replacement := intent
	replacement.StudentID = "990000000002"
	replacement.StudentName = "示例乙"
	if err := database.CreateBindingIntent(replacement); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ConfirmMatchingBindingIntent(*checked, now); !errors.Is(err, ErrBindingConflict) {
		t.Fatalf("stale roster validation consumed a different intent: %v", err)
	}
	if bindings, err := database.ListExternalBindings(); err != nil || len(bindings) != 0 {
		t.Fatalf("stale confirmation created binding: %v %+v", err, bindings)
	}
	current, err := database.GetBindingIntent(intent.PlatformID, intent.BotID, intent.ExternalUserID)
	if err != nil || current.StudentID != replacement.StudentID {
		t.Fatalf("new intent was lost: %+v %v", current, err)
	}
	if err := database.CancelBindingIntentIfMatches(*checked); err != nil {
		t.Fatal(err)
	}
	current, err = database.GetBindingIntent(intent.PlatformID, intent.BotID, intent.ExternalUserID)
	if err != nil || current.StudentID != replacement.StudentID {
		t.Fatalf("stale cancellation deleted refreshed intent: %+v %v", current, err)
	}
	binding, err := database.ConfirmMatchingBindingIntent(*current, now)
	if err != nil || binding.StudentID != replacement.StudentID || binding.Source != "self_asserted" {
		t.Fatalf("current intent was not accepted: %+v %v", binding, err)
	}
	if _, err := database.ConfirmBindingIntent(intent.PlatformID, intent.BotID, intent.ExternalUserID, now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("consumed intent could be confirmed twice: %v", err)
	}
}

func TestBindingIntentExpiryCleanupCannotDeleteReplacement(t *testing.T) {
	database, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	old := BindingIntent{ExternalBinding: ExternalBinding{
		PlatformID: "course-qq", BotID: "bot-example", ExternalUserID: "user-one",
		StudentID: "990000000001", StudentName: "示例甲", ClassName: "示例班A",
	}, ExpiresAt: time.Now().Add(-time.Minute).UTC()}
	if err := database.CreateBindingIntent(old); err != nil {
		t.Fatal(err)
	}
	checked, err := database.GetBindingIntent(old.PlatformID, old.BotID, old.ExternalUserID)
	if err != nil {
		t.Fatal(err)
	}
	refreshed := old
	refreshed.ExpiresAt = time.Now().Add(5 * time.Minute).UTC()
	if err := database.CreateBindingIntent(refreshed); err != nil {
		t.Fatal(err)
	}
	if err := database.CancelBindingIntentIfMatches(*checked); err != nil {
		t.Fatal(err)
	}
	current, err := database.GetBindingIntent(old.PlatformID, old.BotID, old.ExternalUserID)
	if err != nil || !current.ExpiresAt.Equal(refreshed.ExpiresAt) {
		t.Fatalf("expired intent cleanup removed renewal: %+v %v", current, err)
	}
}
