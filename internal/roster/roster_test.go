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
990000000008,男,示例甲,示例班A
990000000009,男,示例乙,示例班A
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

	s, ok := r.FindByID("990000000008")
	if !ok || s.Name != "示例甲" {
		t.Errorf("FindByID failed: %+v", s)
	}

	byName := r.FindByName("示例乙")
	if len(byName) != 1 || byName[0].StudentID != "990000000009" {
		t.Errorf("FindByName failed: %+v", byName)
	}
}
