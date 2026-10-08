package roster

import (
	"os"
	"path/filepath"
	"testing"
)

// [Test]
func TestRosterLoadAndLookup(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "roster_test_*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	csvContent := `学号,性别,姓名,班级
240809010501,男,支全振,2024级计算机科学与技术5班
240809010502,男,马祥宇,2024级计算机科学与技术5班
`
	csvPath := filepath.Join(tmpDir, "class.csv")
	if err := os.WriteFile(csvPath, []byte(csvContent), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	r := New()
	students, err := r.LoadCSV(csvPath)
	if err != nil {
		t.Fatalf("LoadCSV error: %v", err)
	}
	if len(students) != 2 {
		t.Fatalf("expected 2 students, got %d", len(students))
	}

	s, ok := r.FindByID("240809010501")
	if !ok || s.Name != "支全振" {
		t.Errorf("FindByID failed: %+v", s)
	}

	byName := r.FindByName("马祥宇")
	if len(byName) != 1 || byName[0].StudentID != "240809010502" {
		t.Errorf("FindByName failed: %+v", byName)
	}
}
