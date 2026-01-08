package checkpoint

import (
	"database/sql"
	"testing"
	"time"
)

func TestCleanupOldRuns(t *testing.T) {
	state, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	defer state.Close()

	oldSuccess := "old-success"
	oldFailed := "old-failed"
	recentSuccess := "recent-success"
	running := "running"

	for _, runID := range []string{oldSuccess, oldFailed, recentSuccess, running} {
		if err := state.CreateRun(runID, "dbo", "public", map[string]string{"run": runID}, "", ""); err != nil {
			t.Fatalf("CreateRun(%s) error: %v", runID, err)
		}
	}

	if err := state.CompleteRun(oldSuccess, "success", ""); err != nil {
		t.Fatalf("CompleteRun(%s) error: %v", oldSuccess, err)
	}
	if err := state.CompleteRun(oldFailed, "failed", "boom"); err != nil {
		t.Fatalf("CompleteRun(%s) error: %v", oldFailed, err)
	}
	if err := state.CompleteRun(recentSuccess, "success", ""); err != nil {
		t.Fatalf("CompleteRun(%s) error: %v", recentSuccess, err)
	}

	oldTime := time.Now().AddDate(0, 0, -31).Format("2006-01-02 15:04:05")
	if _, err := state.db.Exec(`UPDATE runs SET completed_at = ? WHERE id IN (?, ?)`, oldTime, oldSuccess, oldFailed); err != nil {
		t.Fatalf("update old completed_at error: %v", err)
	}

	recentTime := time.Now().AddDate(0, 0, -1).Format("2006-01-02 15:04:05")
	if _, err := state.db.Exec(`UPDATE runs SET completed_at = ? WHERE id = ?`, recentTime, recentSuccess); err != nil {
		t.Fatalf("update recent completed_at error: %v", err)
	}

	taskIDs := make(map[string]int64)
	for _, runID := range []string{oldSuccess, oldFailed, recentSuccess, running} {
		taskID, err := state.CreateTask(runID, "transfer", "transfer:dbo.Table")
		if err != nil {
			t.Fatalf("CreateTask(%s) error: %v", runID, err)
		}
		taskIDs[runID] = taskID
		if err := state.SaveTransferProgress(taskID, "Table", nil, int64(1), 10, 100); err != nil {
			t.Fatalf("SaveTransferProgress(%s) error: %v", runID, err)
		}
		if _, err := state.db.Exec(`INSERT INTO task_outputs (task_id, key, value) VALUES (?, ?, ?)`, taskID, "k", "v"); err != nil {
			t.Fatalf("insert task_outputs(%s) error: %v", runID, err)
		}
	}

	deleted, err := state.CleanupOldRuns(30)
	if err != nil {
		t.Fatalf("CleanupOldRuns error: %v", err)
	}
	if deleted != 2 {
		t.Fatalf("deleted runs = %d, want 2", deleted)
	}

	if got := countRows(t, state.db, `SELECT COUNT(*) FROM runs`); got != 2 {
		t.Fatalf("runs remaining = %d, want 2", got)
	}
	if got := countRows(t, state.db, `SELECT COUNT(*) FROM runs WHERE id = ?`, running); got != 1 {
		t.Fatalf("running run missing after cleanup")
	}
	if got := countRows(t, state.db, `SELECT COUNT(*) FROM tasks`); got != 2 {
		t.Fatalf("tasks remaining = %d, want 2", got)
	}
	if got := countRows(t, state.db, `SELECT COUNT(*) FROM transfer_progress`); got != 2 {
		t.Fatalf("transfer_progress remaining = %d, want 2", got)
	}
	if got := countRows(t, state.db, `SELECT COUNT(*) FROM task_outputs`); got != 2 {
		t.Fatalf("task_outputs remaining = %d, want 2", got)
	}
}

func countRows(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var count int
	if err := db.QueryRow(query, args...).Scan(&count); err != nil {
		t.Fatalf("count query error: %v", err)
	}
	return count
}
