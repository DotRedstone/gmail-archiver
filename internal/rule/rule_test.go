package rule

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// [Test]
func TestRuleMatchAndExtraction(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "rule_test_*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	csvContent := `学号,性别,姓名,班级
240809010501,男,支全振,2024级计算机科学与技术5班
240809010502,男,马祥宇,2024级计算机科学与技术5班
`
	rosterPath := filepath.Join(tmpDir, "roster.csv")
	if err := os.WriteFile(rosterPath, []byte(csvContent), 0o644); err != nil {
		t.Fatalf("write roster: %v", err)
	}

	yamlContent := `id: "parallel_computing_lab1"
name: "并行计算实验1"
deadline: "2026-09-23T18:00:00+08:00"
rosters:
  - "roster.csv"
patterns:
  subject_regex: "^并行计算-实验1-(?P<name>[\\p{Han}\\w]+)$"
  attachment_regex: "^实验1-(?P<class>[\\w\\p{Han}]+)-(?P<student_id>\\d{12})-(?P<name>[\\p{Han}\\w]+)\\.(?P<ext>zip|rar|7z|tar\\.gz)$"
  body_regexes:
    - "姓名[：:]\\s*(?P<name>[\\p{Han}\\w]+)"
    - "学号[：:]\\s*(?P<student_id>\\d{12})"
    - "班级[：:]\\s*(?P<class>[\\w\\p{Han}]+)"
target_filename: "实验1-{class}-{student_id}-{name}.{ext}"
`
	rulePath := filepath.Join(tmpDir, "lab1.yaml")
	if err := os.WriteFile(rulePath, []byte(yamlContent), 0o644); err != nil {
		t.Fatalf("write rule: %v", err)
	}

	engine := NewEngine(tmpDir)
	rule, err := engine.LoadRuleFile("lab1.yaml")
	if err != nil {
		t.Fatalf("LoadRuleFile: %v", err)
	}

	// 1. Standard submission matching
	recvAt := time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC)
	matchRule, res, ok := engine.Match(
		"并行计算-实验1-支全振",
		"实验1-241-240809010501-支全振.zip",
		"姓名：支全振\n学号：240809010501",
		recvAt,
	)
	if !ok || matchRule.ID != "parallel_computing_lab1" {
		t.Fatalf("expected match, got ok=%v", ok)
	}
	if res.StudentID != "240809010501" || res.StudentName != "支全振" || res.IsLate {
		t.Errorf("unexpected match result: %+v", res)
	}
	if res.TargetFilename != "实验1-241-240809010501-支全振.zip" {
		t.Errorf("unexpected target filename: %s", res.TargetFilename)
	}

	// 2. Late submission
	lateAt := time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC)
	lateRes, ok := rule.Match(
		"并行计算-实验1-马祥宇",
		"实验1-241-240809010502-马祥宇.zip",
		"",
		lateAt,
	)
	if !ok || !lateRes.IsLate {
		t.Errorf("expected late match, got ok=%v, isLate=%v", ok, lateRes.IsLate)
	}

	// 3. NormalizeSubmission directly (for QQ / Web upload)
	finalID, finalName, finalClass, targetName, isLate, normErr := rule.NormalizeSubmission(
		"240809010501", "", "", "any_random_name.zip", recvAt,
	)
	if normErr != nil {
		t.Fatalf("NormalizeSubmission: %v", normErr)
	}
	if finalID != "240809010501" || finalName != "支全振" || finalClass != "2024级计算机科学与技术5班" {
		t.Errorf("expected auto roster lookup, got %s, %s, %s", finalID, finalName, finalClass)
	}
	if targetName != "实验1-2024级计算机科学与技术5班-240809010501-支全振.zip" || isLate {
		t.Errorf("unexpected targetName=%s, isLate=%v", targetName, isLate)
	}

	// 4. Test LatestRule
	latest := engine.LatestRule()
	if latest == nil || latest.ID != "parallel_computing_lab1" {
		t.Errorf("expected latest rule parallel_computing_lab1, got %+v", latest)
	}
}

