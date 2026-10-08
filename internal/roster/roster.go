package roster

import (
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
)

// [Student]
type Student struct {
	StudentID string `json:"student_id"`
	Gender    string `json:"gender"`
	Name      string `json:"name"`
	ClassName string `json:"class_name"`
}

// [Roster]
type Roster struct {
	mu       sync.RWMutex
	byID     map[string]Student
	byName   map[string][]Student
	students []Student
}

func New() *Roster {
	return &Roster{
		byID:     make(map[string]Student),
		byName:   make(map[string][]Student),
		students: make([]Student, 0),
	}
}

// [Loader]
func (r *Roster) LoadCSV(path string) ([]Student, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("roster: open file %q: %w", path, err)
	}
	defer file.Close()

	reader := csv.NewReader(file)
	reader.FieldsPerRecord = -1 // Allow variable fields if needed
	reader.TrimLeadingSpace = true

	headers, err := reader.Read()
	if err != nil {
		return nil, fmt.Errorf("roster: read headers: %w", err)
	}

	// Map header names to column index
	idCol := -1
	nameCol := -1
	genderCol := -1
	classCol := -1

	for i, h := range headers {
		clean := strings.TrimSpace(strings.TrimPrefix(h, "\ufeff"))
		switch clean {
		case "学号", "student_id", "id":
			idCol = i
		case "姓名", "name":
			nameCol = i
		case "性别", "gender":
			genderCol = i
		case "班级", "class", "class_name":
			classCol = i
		}
	}

	if idCol == -1 || nameCol == -1 {
		return nil, fmt.Errorf("roster: CSV must contain '学号' and '姓名' columns")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	var loaded []Student
	lineNum := 1
	for {
		lineNum++
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("roster: read line %d: %w", lineNum, err)
		}

		studentID := ""
		if idCol < len(record) {
			studentID = strings.TrimSpace(record[idCol])
		}
		name := ""
		if nameCol < len(record) {
			name = strings.TrimSpace(record[nameCol])
		}
		if studentID == "" || name == "" {
			continue
		}

		gender := ""
		if genderCol >= 0 && genderCol < len(record) {
			gender = strings.TrimSpace(record[genderCol])
		}
		className := ""
		if classCol >= 0 && classCol < len(record) {
			className = strings.TrimSpace(record[classCol])
		}

		s := Student{
			StudentID: studentID,
			Gender:    gender,
			Name:      name,
			ClassName: className,
		}

		// Store in index
		if _, exists := r.byID[studentID]; !exists {
			r.students = append(r.students, s)
		}
		r.byID[studentID] = s
		r.byName[name] = append(r.byName[name], s)
		loaded = append(loaded, s)
	}

	return loaded, nil
}

// [Lookup]
func (r *Roster) FindByID(studentID string) (Student, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.byID[studentID]
	return s, ok
}

func (r *Roster) FindByName(name string) []Student {
	r.mu.RLock()
	defer r.mu.RUnlock()
	list, ok := r.byName[name]
	if !ok {
		return nil
	}
	res := make([]Student, len(list))
	copy(res, list)
	return res
}

func (r *Roster) All() []Student {
	r.mu.RLock()
	defer r.mu.RUnlock()
	res := make([]Student, len(r.students))
	copy(res, r.students)
	return res
}

func (r *Roster) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.students)
}
