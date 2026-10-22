package api

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/dot/gmail-archiver/internal/roster"
)

var errRosterUnavailable = errors.New("course roster unavailable")

// courseStudents keeps the loaded assignment roster authoritative. Before the
// first assignment exists, standalone CSV files under data/rosters allow
// identity matching without fabricating an assignment rule.
func (s *Server) courseStudents() ([]roster.Student, error) {
	var groups [][]roster.Student
	if s.rules != nil {
		for _, assignment := range s.rules.Rules() {
			groups = append(groups, assignment.Roster.All())
		}
	}
	if len(groups) == 0 {
		if s.cfg == nil || s.cfg.DataDir == "" {
			return nil, errRosterUnavailable
		}
		entries, err := os.ReadDir(filepath.Join(s.cfg.DataDir, "rosters"))
		if err != nil {
			return nil, errRosterUnavailable
		}
		for _, entry := range entries {
			if !entry.Type().IsRegular() || !strings.EqualFold(filepath.Ext(entry.Name()), ".csv") {
				continue
			}
			list := roster.New()
			loaded, err := list.LoadCSV(filepath.Join(s.cfg.DataDir, "rosters", entry.Name()))
			if err != nil {
				return nil, errRosterUnavailable
			}
			groups = append(groups, loaded)
		}
	}
	if len(groups) == 0 {
		return nil, errRosterUnavailable
	}
	byID := make(map[string]roster.Student)
	for _, group := range groups {
		for _, student := range group {
			student.ClassName = roster.NormalizeClassName(student.ClassName)
			if previous, found := byID[student.StudentID]; found &&
				(previous.Name != student.Name || previous.ClassName != student.ClassName) {
				return nil, errRosterUnavailable
			}
			byID[student.StudentID] = student
		}
	}
	students := make([]roster.Student, 0, len(byID))
	for _, student := range byID {
		students = append(students, student)
	}
	sort.Slice(students, func(i, j int) bool { return students[i].StudentID < students[j].StudentID })
	return students, nil
}
