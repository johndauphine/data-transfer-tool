package progress

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/schollz/progressbar/v3"
)

// Tracker tracks migration progress
type Tracker struct {
	bar       *progressbar.ProgressBar
	total     int64
	current   atomic.Int64
	startTime time.Time

	// Track active tables for accurate display
	mu           sync.Mutex
	activeTables map[string]int // table name -> active job count
}

// New creates a new progress tracker
func New() *Tracker {
	return &Tracker{
		startTime:    time.Now(),
		activeTables: make(map[string]int),
	}
}

// SetTotal sets the total number of rows to transfer
func (t *Tracker) SetTotal(total int64) {
	t.total = total
	t.bar = progressbar.NewOptions64(
		total,
		progressbar.OptionSetDescription("Transferring"),
		progressbar.OptionShowBytes(false),
		progressbar.OptionShowCount(),
		progressbar.OptionSetWidth(40),
		progressbar.OptionThrottle(100*time.Millisecond),
		progressbar.OptionShowIts(),
		progressbar.OptionSetItsString("rows"),
		progressbar.OptionSpinnerType(14),
		progressbar.OptionFullWidth(),
		progressbar.OptionSetRenderBlankState(true),
	)
}

// Add increments the progress counter
func (t *Tracker) Add(n int64) {
	t.current.Add(n)
	if t.bar != nil {
		t.bar.Add64(n)
	}
}

// StartTable marks a table as actively transferring
func (t *Tracker) StartTable(tableName string) {
	t.mu.Lock()
	t.activeTables[tableName]++
	tableCount := len(t.activeTables)
	t.mu.Unlock()

	if t.bar != nil {
		if tableCount == 1 {
			t.bar.Describe(fmt.Sprintf("Transferring %s", tableName))
		} else {
			t.bar.Describe(fmt.Sprintf("Transferring (%d tables)", tableCount))
		}
		t.bar.RenderBlank()
	}
}

// EndTable marks a table job as done transferring
func (t *Tracker) EndTable(tableName string) {
	t.mu.Lock()
	t.activeTables[tableName]--
	if t.activeTables[tableName] <= 0 {
		delete(t.activeTables, tableName)
	}
	tableCount := len(t.activeTables)
	// Get remaining table name if only one left
	var remaining string
	for name := range t.activeTables {
		remaining = name
		break
	}
	t.mu.Unlock()

	if t.bar != nil && tableCount > 0 {
		if tableCount == 1 {
			t.bar.Describe(fmt.Sprintf("Transferring %s", remaining))
		} else {
			t.bar.Describe(fmt.Sprintf("Transferring (%d tables)", tableCount))
		}
	}
}

// SetTable updates the progress bar description with the current table name
// Deprecated: Use StartTable/EndTable for accurate multi-table tracking
func (t *Tracker) SetTable(tableName string) {
	t.StartTable(tableName)
}

// Current returns the current count
func (t *Tracker) Current() int64 {
	return t.current.Load()
}

// Finish marks the progress as complete
func (t *Tracker) Finish() {
	if t.bar != nil {
		t.bar.Finish()
	}

	elapsed := time.Since(t.startTime)
	rowsPerSec := float64(t.current.Load()) / elapsed.Seconds()

	fmt.Println()
	fmt.Printf("Transferred %d rows in %s (%.0f rows/sec)\n",
		t.current.Load(), elapsed.Round(time.Second), rowsPerSec)
}
