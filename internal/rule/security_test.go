package rule

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRuleWriteCannotEscapeDirectory(t *testing.T) {
	base := t.TempDir()
	engine := NewEngine(base)
	for _, id := range []string{"../escape", "/escape", "a/b", "a\\b", ".."} {
		if _, err := engine.SaveAndLoadRule(RuleFileConfig{ID: id, Name: "合成作业"}, filepath.Join(base, "rules")); err == nil {
			t.Fatalf("accepted unsafe ID %q", id)
		}
	}
	if _, err := os.Stat(filepath.Join(base, "escape.yaml")); !os.IsNotExist(err) {
		t.Fatal("wrote outside rules directory")
	}
	rules := filepath.Join(base, "rules")
	os.MkdirAll(rules, 0700)
	outside := filepath.Join(base, "outside.yaml")
	os.WriteFile(outside, []byte("sentinel"), 0600)
	os.Symlink(outside, filepath.Join(rules, "safe.yaml"))
	if _, err := engine.SaveAndLoadRule(RuleFileConfig{ID: "safe", Name: "合成作业"}, rules); err == nil {
		t.Fatal("followed outside symlink")
	}
	data, _ := os.ReadFile(outside)
	if string(data) != "sentinel" {
		t.Fatal("overwrote outside file")
	}
}
