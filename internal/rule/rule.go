package rule

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/dot/gmail-archiver/internal/roster"
)

// [Config]
type RuleFileConfig struct {
	ID             string   `yaml:"id"`
	Name           string   `yaml:"name"`
	Deadline       string   `yaml:"deadline"`
	Rosters        []string `yaml:"rosters"`
	TargetFilename string   `yaml:"target_filename"`
	Patterns       struct {
		SubjectRegex    string   `yaml:"subject_regex"`
		AttachmentRegex string   `yaml:"attachment_regex"`
		BodyRegexes     []string `yaml:"body_regexes"`
	} `yaml:"patterns"`
}

// [Rule]
type AssignmentRule struct {
	ID             string
	Name           string
	Deadline       time.Time
	TargetFilename string
	RosterPaths    []string
	Roster         *roster.Roster

	subjectRe    *regexp.Regexp
	attachmentRe *regexp.Regexp
	bodyRes      []*regexp.Regexp
}

// [MatchResult]
type MatchResult struct {
	AssignmentID   string
	StudentID      string
	StudentName    string
	ClassName      string
	TargetFilename string
	IsLate         bool
	MatchedBy      string
}

// [Engine]
type Engine struct {
	mu      sync.RWMutex
	baseDir string
	rules   []*AssignmentRule
}

func NewEngine(baseDir string) *Engine {
	return &Engine{
		baseDir: baseDir,
		rules:   make([]*AssignmentRule, 0),
	}
}

// [Load]
func (e *Engine) LoadRuleFile(rulePath string) (*AssignmentRule, error) {
	absRulePath := resolvePath(e.baseDir, rulePath)

	data, err := os.ReadFile(absRulePath)
	if err != nil {
		return nil, fmt.Errorf("rule: read file %q: %w", absRulePath, err)
	}

	var raw RuleFileConfig
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("rule: parse yaml: %w", err)
	}

	if raw.ID == "" {
		return nil, fmt.Errorf("rule: missing required field 'id'")
	}

	rule := &AssignmentRule{
		ID:             raw.ID,
		Name:           raw.Name,
		TargetFilename: raw.TargetFilename,
		RosterPaths:    raw.Rosters,
		Roster:         roster.New(),
	}

	if raw.Deadline != "" {
		dl, err := time.Parse(time.RFC3339, raw.Deadline)
		if err == nil {
			rule.Deadline = dl.UTC()
		}
	}

	// Compile regexes
	if raw.Patterns.SubjectRegex != "" {
		re, err := regexp.Compile(raw.Patterns.SubjectRegex)
		if err != nil {
			return nil, fmt.Errorf("rule: compile subject regex: %w", err)
		}
		rule.subjectRe = re
	}

	if raw.Patterns.AttachmentRegex != "" {
		re, err := regexp.Compile(raw.Patterns.AttachmentRegex)
		if err != nil {
			return nil, fmt.Errorf("rule: compile attachment regex: %w", err)
		}
		rule.attachmentRe = re
	}

	for _, br := range raw.Patterns.BodyRegexes {
		re, err := regexp.Compile(br)
		if err != nil {
			return nil, fmt.Errorf("rule: compile body regex %q: %w", br, err)
		}
		rule.bodyRes = append(rule.bodyRes, re)
	}

	// Load associated roster CSV files
	for _, rp := range raw.Rosters {
		absRoster := resolveRosterPath(filepath.Dir(absRulePath), e.baseDir, rp)
		if _, err := rule.Roster.LoadCSV(absRoster); err != nil {
			return nil, fmt.Errorf("rule: load roster %q: %w", absRoster, err)
		}
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	// Replace existing rule with same ID or append
	replaced := false
	for i, r := range e.rules {
		if r.ID == rule.ID {
			e.rules[i] = rule
			replaced = true
			break
		}
	}
	if !replaced {
		e.rules = append(e.rules, rule)
	}

	return rule, nil
}

// [Rules]
func (e *Engine) Rules() []*AssignmentRule {
	e.mu.RLock()
	defer e.mu.RUnlock()
	res := make([]*AssignmentRule, len(e.rules))
	copy(res, e.rules)
	return res
}

func (e *Engine) GetRule(id string) (*AssignmentRule, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	for _, r := range e.rules {
		if r.ID == id {
			return r, true
		}
	}
	return nil, false
}

func (e *Engine) LatestRule() *AssignmentRule {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if len(e.rules) == 0 {
		return nil
	}

	now := time.Now().UTC()
	var unexpired []*AssignmentRule
	for _, r := range e.rules {
		if r.Deadline.IsZero() || r.Deadline.After(now) {
			unexpired = append(unexpired, r)
		}
	}

	if len(unexpired) > 0 {
		return unexpired[len(unexpired)-1]
	}

	return e.rules[len(e.rules)-1]
}

func (e *Engine) SaveAndLoadRule(cfg RuleFileConfig, rulesDir string) (*AssignmentRule, error) {
	if cfg.ID == "" {
		return nil, fmt.Errorf("rule id is required")
	}
	if err := os.MkdirAll(rulesDir, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir rules dir: %w", err)
	}

	filePath := filepath.Join(rulesDir, cfg.ID+".yaml")
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return nil, fmt.Errorf("marshal rule yaml: %w", err)
	}

	if err := os.WriteFile(filePath, data, 0o644); err != nil {
		return nil, fmt.Errorf("write rule file: %w", err)
	}

	return e.LoadRuleFile(filePath)
}

// [Match]
func (e *Engine) Match(subject, filename, bodyText string, receivedAt time.Time) (*AssignmentRule, *MatchResult, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	for _, rule := range e.rules {
		res, ok := rule.Match(subject, filename, bodyText, receivedAt)
		if ok {
			return rule, res, true
		}
	}
	return nil, nil, false
}

func (r *AssignmentRule) Match(subject, filename, bodyText string, receivedAt time.Time) (*MatchResult, bool) {
	extracted := make(map[string]string)
	matchedBy := ""

	// 1. Try attachment filename regex
	if r.attachmentRe != nil {
		matches := extractNamedGroups(r.attachmentRe, filename)
		if len(matches) == 0 {
			return nil, false
		}
		for k, v := range matches {
			extracted[k] = v
		}
		matchedBy = "attachment_regex"
	}

	// 2. Try subject regex
	if r.subjectRe != nil {
		if matches := extractNamedGroups(r.subjectRe, subject); len(matches) > 0 {
			for k, v := range matches {
				if _, exists := extracted[k]; !exists {
					extracted[k] = v
				}
			}
			if matchedBy == "" {
				matchedBy = "subject_regex"
			}
		}
	}

	// If neither attachment nor subject has any match with rule, skip
	if matchedBy == "" {
		return nil, false
	}

	// 3. Try body regexes to supplement missing fields
	for _, re := range r.bodyRes {
		matches := extractNamedGroups(re, bodyText)
		for k, v := range matches {
			if _, exists := extracted[k]; !exists {
				extracted[k] = v
			}
		}
	}

	studentID := strings.TrimSpace(extracted["student_id"])
	name := strings.TrimSpace(extracted["name"])
	className := strings.TrimSpace(extracted["class"])

	// Validate against roster
	var student *roster.Student
	if studentID != "" {
		if s, ok := r.Roster.FindByID(studentID); ok {
			student = &s
		}
	} else if name != "" {
		list := r.Roster.FindByName(name)
		if len(list) == 1 {
			student = &list[0]
		}
	}

	if student == nil {
		return nil, false // Not in this assignment's roster
	}

	// Fill normalized student info
	finalID := student.StudentID
	finalName := student.Name
	finalClass := student.ClassName
	if className != "" {
		finalClass = className
	}

	ext := strings.TrimPrefix(filepath.Ext(filename), ".")
	if extractedExt, ok := extracted["ext"]; ok && extractedExt != "" {
		ext = extractedExt
	}

	// Generate target formatted filename
	targetName := filename
	if r.TargetFilename != "" {
		tn := r.TargetFilename
		tn = strings.ReplaceAll(tn, "{class}", finalClass)
		tn = strings.ReplaceAll(tn, "{student_id}", finalID)
		tn = strings.ReplaceAll(tn, "{name}", finalName)
		tn = strings.ReplaceAll(tn, "{ext}", ext)
		targetName = tn
	}

	isLate := false
	if !r.Deadline.IsZero() && receivedAt.After(r.Deadline) {
		isLate = true
	}

	return &MatchResult{
		AssignmentID:   r.ID,
		StudentID:      finalID,
		StudentName:    finalName,
		ClassName:      finalClass,
		TargetFilename: targetName,
		IsLate:         isLate,
		MatchedBy:      matchedBy,
	}, true
}

func (r *AssignmentRule) NormalizeSubmission(studentID, studentName, className, originalFilename string, submittedAt time.Time) (finalID, finalName, finalClass, targetFilename string, isLate bool, err error) {
	var student *roster.Student
	if studentID != "" {
		if s, ok := r.Roster.FindByID(studentID); ok {
			student = &s
		}
	}
	if student == nil && studentName != "" {
		list := r.Roster.FindByName(studentName)
		if len(list) == 1 {
			student = &list[0]
		}
	}

	if student != nil {
		finalID = student.StudentID
		finalName = student.Name
		finalClass = student.ClassName
		if className != "" {
			finalClass = className
		}
	} else {
		finalID = strings.TrimSpace(studentID)
		finalName = strings.TrimSpace(studentName)
		finalClass = strings.TrimSpace(className)
		if finalID == "" && finalName == "" {
			return "", "", "", "", false, fmt.Errorf("student identification (id or name) required")
		}
	}

	ext := "zip"
	origLower := strings.ToLower(originalFilename)
	if strings.HasSuffix(origLower, ".tar.gz") {
		ext = "tar.gz"
	} else if dotExt := filepath.Ext(originalFilename); dotExt != "" {
		ext = strings.TrimPrefix(dotExt, ".")
	}

	targetName := originalFilename
	if r.TargetFilename != "" {
		tn := r.TargetFilename
		tn = strings.ReplaceAll(tn, "{class}", finalClass)
		tn = strings.ReplaceAll(tn, "{student_id}", finalID)
		tn = strings.ReplaceAll(tn, "{name}", finalName)
		tn = strings.ReplaceAll(tn, "{ext}", ext)
		targetName = tn
	}

	if !r.Deadline.IsZero() && submittedAt.After(r.Deadline) {
		isLate = true
	}

	return finalID, finalName, finalClass, targetName, isLate, nil
}


func extractNamedGroups(re *regexp.Regexp, s string) map[string]string {
	match := re.FindStringSubmatch(s)
	if match == nil {
		return nil
	}
	result := make(map[string]string)
	for i, name := range re.SubexpNames() {
		if i != 0 && name != "" && i < len(match) {
			result[name] = match[i]
		}
	}
	return result
}

func resolvePath(baseDir, p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	if _, err := os.Stat(p); err == nil {
		return p
	}
	cand := filepath.Join(baseDir, p)
	if _, err := os.Stat(cand); err == nil {
		return cand
	}
	return cand
}

func resolveRosterPath(ruleDir, baseDir, p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	// Try relative to rule file directory
	candRule := filepath.Join(ruleDir, p)
	if _, err := os.Stat(candRule); err == nil {
		return candRule
	}
	// Try relative to baseDir
	candBase := filepath.Join(baseDir, p)
	if _, err := os.Stat(candBase); err == nil {
		return candBase
	}
	// Try relative to current working directory
	if _, err := os.Stat(p); err == nil {
		return p
	}
	return candBase
}
