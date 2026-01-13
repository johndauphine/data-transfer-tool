package progress

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestNewJSONReporter(t *testing.T) {
	t.Run("with writer", func(t *testing.T) {
		var buf bytes.Buffer
		reporter := NewJSONReporter(&buf, time.Second)

		if reporter == nil {
			t.Fatal("expected reporter, got nil")
		}
		if reporter.writer != &buf {
			t.Error("expected writer to be set")
		}
		if reporter.interval != time.Second {
			t.Errorf("interval = %v, want %v", reporter.interval, time.Second)
		}
	})

	t.Run("nil writer defaults to stderr", func(t *testing.T) {
		reporter := NewJSONReporter(nil, 0)

		if reporter == nil {
			t.Fatal("expected reporter, got nil")
		}
		// Can't easily test os.Stderr assignment, just verify it doesn't panic
	})
}

func TestJSONReporter_Report(t *testing.T) {
	t.Run("outputs valid JSON", func(t *testing.T) {
		var buf bytes.Buffer
		reporter := NewJSONReporter(&buf, 0)

		update := ProgressUpdate{
			Phase:           "transferring",
			TablesComplete:  5,
			TablesTotal:     10,
			TablesRunning:   2,
			RowsTransferred: 50000,
			RowsTotal:       100000,
			ProgressPct:     50.0,
			RowsPerSecond:   10000,
			CurrentTables:   []string{"users", "posts"},
			ErrorCount:      0,
		}

		reporter.Report(update)

		output := strings.TrimSpace(buf.String())
		var parsed ProgressUpdate
		if err := json.Unmarshal([]byte(output), &parsed); err != nil {
			t.Fatalf("failed to parse JSON output: %v\nOutput: %s", err, output)
		}

		if parsed.Phase != "transferring" {
			t.Errorf("phase = %q, want %q", parsed.Phase, "transferring")
		}
		if parsed.TablesComplete != 5 {
			t.Errorf("tables_complete = %d, want 5", parsed.TablesComplete)
		}
		if parsed.RowsTransferred != 50000 {
			t.Errorf("rows_transferred = %d, want 50000", parsed.RowsTransferred)
		}
		if len(parsed.CurrentTables) != 2 {
			t.Errorf("current_tables length = %d, want 2", len(parsed.CurrentTables))
		}
	})

	t.Run("sets timestamp if not provided", func(t *testing.T) {
		var buf bytes.Buffer
		reporter := NewJSONReporter(&buf, 0)

		update := ProgressUpdate{Phase: "testing"}
		reporter.Report(update)

		var parsed ProgressUpdate
		json.Unmarshal([]byte(strings.TrimSpace(buf.String())), &parsed)

		if parsed.Timestamp == "" {
			t.Error("expected timestamp to be set")
		}

		// Verify it's a valid RFC3339 timestamp
		_, err := time.Parse(time.RFC3339, parsed.Timestamp)
		if err != nil {
			t.Errorf("invalid timestamp format: %v", err)
		}
	})

	t.Run("preserves provided timestamp", func(t *testing.T) {
		var buf bytes.Buffer
		reporter := NewJSONReporter(&buf, 0)

		customTime := "2026-01-12T10:00:00Z"
		update := ProgressUpdate{
			Timestamp: customTime,
			Phase:     "testing",
		}
		reporter.Report(update)

		var parsed ProgressUpdate
		json.Unmarshal([]byte(strings.TrimSpace(buf.String())), &parsed)

		if parsed.Timestamp != customTime {
			t.Errorf("timestamp = %q, want %q", parsed.Timestamp, customTime)
		}
	})
}

func TestJSONReporter_Report_Throttling(t *testing.T) {
	var buf bytes.Buffer
	reporter := NewJSONReporter(&buf, 100*time.Millisecond)

	// First report should go through
	reporter.Report(ProgressUpdate{Phase: "first"})
	firstOutput := buf.String()
	if !strings.Contains(firstOutput, "first") {
		t.Error("first report should go through")
	}

	// Immediate second report should be throttled
	buf.Reset()
	reporter.Report(ProgressUpdate{Phase: "second"})
	if buf.String() != "" {
		t.Error("second report should be throttled")
	}

	// Wait for throttle period
	time.Sleep(150 * time.Millisecond)

	// Third report should go through
	buf.Reset()
	reporter.Report(ProgressUpdate{Phase: "third"})
	if !strings.Contains(buf.String(), "third") {
		t.Error("third report should go through after throttle period")
	}
}

func TestJSONReporter_Report_NoThrottling(t *testing.T) {
	var buf bytes.Buffer
	reporter := NewJSONReporter(&buf, 0) // No throttling

	// Multiple reports should all go through
	for i := 0; i < 5; i++ {
		reporter.Report(ProgressUpdate{Phase: "test"})
	}

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 5 {
		t.Errorf("expected 5 lines, got %d", len(lines))
	}
}

func TestJSONReporter_ReportImmediate(t *testing.T) {
	t.Run("bypasses throttling", func(t *testing.T) {
		var buf bytes.Buffer
		reporter := NewJSONReporter(&buf, 1*time.Hour) // Very long throttle

		// First normal report
		reporter.Report(ProgressUpdate{Phase: "first"})

		// Immediate report should bypass throttle
		reporter.ReportImmediate(ProgressUpdate{Phase: "immediate"})

		output := buf.String()
		if !strings.Contains(output, "immediate") {
			t.Error("immediate report should bypass throttling")
		}
	})

	t.Run("sets timestamp if not provided", func(t *testing.T) {
		var buf bytes.Buffer
		reporter := NewJSONReporter(&buf, 0)

		reporter.ReportImmediate(ProgressUpdate{Phase: "testing"})

		var parsed ProgressUpdate
		json.Unmarshal([]byte(strings.TrimSpace(buf.String())), &parsed)

		if parsed.Timestamp == "" {
			t.Error("expected timestamp to be set")
		}
	})

	t.Run("updates lastReport time", func(t *testing.T) {
		var buf bytes.Buffer
		reporter := NewJSONReporter(&buf, 100*time.Millisecond)

		reporter.ReportImmediate(ProgressUpdate{Phase: "immediate"})

		// Next regular report should be throttled
		buf.Reset()
		reporter.Report(ProgressUpdate{Phase: "throttled"})
		if buf.String() != "" {
			t.Error("report after immediate should be throttled")
		}
	})
}

func TestJSONReporter_Close(t *testing.T) {
	var buf bytes.Buffer
	reporter := NewJSONReporter(&buf, 0)

	reporter.Close()

	if !reporter.closed {
		t.Error("expected reporter to be closed")
	}

	// Reports after close should be ignored
	buf.Reset()
	reporter.Report(ProgressUpdate{Phase: "after-close"})
	if buf.String() != "" {
		t.Error("report after close should be ignored")
	}

	reporter.ReportImmediate(ProgressUpdate{Phase: "immediate-after-close"})
	if buf.String() != "" {
		t.Error("immediate report after close should be ignored")
	}
}

func TestJSONReporter_Concurrent(t *testing.T) {
	var buf bytes.Buffer
	reporter := NewJSONReporter(&buf, 0)

	done := make(chan bool)
	numGoroutines := 10
	reportsPerGoroutine := 100

	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			for j := 0; j < reportsPerGoroutine; j++ {
				reporter.Report(ProgressUpdate{
					Phase:          "testing",
					TablesComplete: id,
				})
			}
			done <- true
		}(i)
	}

	// Wait for all goroutines
	for i := 0; i < numGoroutines; i++ {
		<-done
	}

	// Should not panic and output should be valid JSON lines
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	for i, line := range lines {
		if line == "" {
			continue
		}
		var update ProgressUpdate
		if err := json.Unmarshal([]byte(line), &update); err != nil {
			t.Errorf("line %d: invalid JSON: %v", i, err)
		}
	}
}

func TestNullReporter(t *testing.T) {
	reporter := &NullReporter{}

	// These should not panic
	reporter.Report(ProgressUpdate{Phase: "test"})
	reporter.ReportImmediate(ProgressUpdate{Phase: "test"})
	reporter.Close()
}

func TestProgressUpdate_JSONSerialization(t *testing.T) {
	update := ProgressUpdate{
		Timestamp:       "2026-01-12T10:00:00Z",
		Phase:           "transferring",
		TablesComplete:  5,
		TablesTotal:     10,
		TablesRunning:   2,
		RowsTransferred: 1000000,
		RowsTotal:       2000000,
		ProgressPct:     50.0,
		RowsPerSecond:   100000,
		CurrentTables:   []string{"users", "posts"},
		ErrorCount:      1,
	}

	data, err := json.Marshal(update)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	var parsed ProgressUpdate
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if parsed.Phase != update.Phase {
		t.Errorf("phase mismatch")
	}
	if parsed.RowsTransferred != update.RowsTransferred {
		t.Errorf("rows_transferred mismatch")
	}
	if parsed.ProgressPct != update.ProgressPct {
		t.Errorf("progress_pct mismatch")
	}
	if len(parsed.CurrentTables) != len(update.CurrentTables) {
		t.Errorf("current_tables length mismatch")
	}
}

func TestProgressUpdate_OmitEmpty(t *testing.T) {
	update := ProgressUpdate{
		Phase:           "testing",
		TablesComplete:  0,
		TablesTotal:     0,
		RowsTransferred: 0,
		// RowsTotal, RowsPerSecond, CurrentTables, ErrorCount should be omitted
	}

	data, _ := json.Marshal(update)
	str := string(data)

	// Fields with omitempty should not appear when zero/empty
	if strings.Contains(str, "rows_total") {
		t.Error("rows_total should be omitted when zero")
	}
	if strings.Contains(str, "rows_per_second") {
		t.Error("rows_per_second should be omitted when zero")
	}
	if strings.Contains(str, "current_tables") {
		t.Error("current_tables should be omitted when nil")
	}
	if strings.Contains(str, "error_count") {
		t.Error("error_count should be omitted when zero")
	}

	// These should always appear
	if !strings.Contains(str, "phase") {
		t.Error("phase should always appear")
	}
	if !strings.Contains(str, "tables_complete") {
		t.Error("tables_complete should always appear")
	}
}
