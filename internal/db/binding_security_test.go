package db

import (
	"fmt"
	"sync"
	"testing"
)

func TestConcurrentStaffBindingKeepsOneOwner(t *testing.T) {
	database, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var workers sync.WaitGroup
	for i := 0; i < 16; i++ {
		workers.Add(1)
		go func(i int) {
			defer workers.Done()
			if err := database.UpsertStudentBinding(&StudentBinding{QQID: fmt.Sprintf("900000%04d", i), StudentID: "990000000001", StudentName: "示例甲"}); err != nil {
				t.Error(err)
			}
		}(i)
	}
	workers.Wait()
	bindings, err := database.ListStudentBindings()
	if err != nil || len(bindings) != 1 {
		t.Fatalf("expected one owner, got %d, err=%v", len(bindings), err)
	}
}
