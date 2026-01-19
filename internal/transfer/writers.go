package transfer

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/johndauphine/dmt/internal/pool"
	"github.com/johndauphine/dmt/internal/progress"
	"github.com/johndauphine/dmt/internal/target"
)

// writerPool manages a pool of parallel write workers.
// It consolidates the common logic from executeKeysetPagination and executeRowNumberPagination.
type writerPool struct {
	// Configuration
	numWriters   int
	bufferSize   int
	useUpsert    bool
	targetSchema string
	targetTable  string
	targetCols   []string
	colTypes     []string
	colSRIDs     []int
	targetPKCols []string
	partitionID  *int
	tgtPool      pool.TargetPool
	prog         *progress.Tracker

	// Channels
	jobChan chan writeJob
	ackChan chan writeAck

	// State
	totalWriteTime int64 // atomic, nanoseconds
	totalWritten   int64 // atomic, rows written
	writeErr       atomic.Pointer[error]

	// Synchronization
	writerWg sync.WaitGroup
	ackWg    sync.WaitGroup
	ctx      context.Context
	cancel   context.CancelFunc
}

// writerPoolConfig holds the configuration for creating a writer pool.
type writerPoolConfig struct {
	NumWriters   int
	BufferSize   int
	UseUpsert    bool
	TargetSchema string
	TargetTable  string
	TargetCols   []string
	ColTypes     []string
	ColSRIDs     []int
	TargetPKCols []string
	PartitionID  *int
	TgtPool      pool.TargetPool
	Prog         *progress.Tracker
	EnableAck    bool // Whether to enable ack channel for checkpointing
}

// newWriterPool creates a new writer pool with the given configuration.
func newWriterPool(ctx context.Context, cfg writerPoolConfig) *writerPool {
	writerCtx, cancel := context.WithCancel(ctx)

	// Use a larger buffer for jobChan to prevent consumer from blocking.
	// With parallel readers producing chunks faster than writers can consume,
	// a small buffer causes the consumer to block on submit(), which blocks
	// reading from chunkChan, which blocks readers, causing deadlock.
	jobBufferSize := cfg.BufferSize * cfg.NumWriters * 50
	if jobBufferSize < 500 {
		jobBufferSize = 500
	}

	wp := &writerPool{
		numWriters:   cfg.NumWriters,
		bufferSize:   cfg.BufferSize,
		useUpsert:    cfg.UseUpsert,
		targetSchema: cfg.TargetSchema,
		targetTable:  cfg.TargetTable,
		targetCols:   cfg.TargetCols,
		colTypes:     cfg.ColTypes,
		colSRIDs:     cfg.ColSRIDs,
		targetPKCols: cfg.TargetPKCols,
		partitionID:  cfg.PartitionID,
		tgtPool:      cfg.TgtPool,
		prog:         cfg.Prog,
		jobChan:      make(chan writeJob, jobBufferSize),
		ctx:          writerCtx,
		cancel:       cancel,
	}

	if cfg.EnableAck {
		// Use a much larger buffer for ackChan to prevent writers from blocking.
		// With parallel readers, writers can produce acks faster than the ack processor
		// can consume them (especially during checkpoint saves). A small buffer causes
		// a cascading deadlock: writers block → jobChan fills → consumer blocks →
		// chunkChan fills → all readers block.
		// Acks are small (just PK values and sequence numbers), so a large buffer is cheap.
		ackBufferSize := cfg.BufferSize * cfg.NumWriters * 100
		if ackBufferSize < 1000 {
			ackBufferSize = 1000
		}
		wp.ackChan = make(chan writeAck, ackBufferSize)
	}

	return wp
}

// start begins the writer worker goroutines.
func (wp *writerPool) start() {
	for i := 0; i < wp.numWriters; i++ {
		writerID := i
		wp.writerWg.Add(1)
		go wp.worker(writerID)
	}
}

// worker is the main write worker goroutine.
func (wp *writerPool) worker(writerID int) {
	defer wp.writerWg.Done()

	for job := range wp.jobChan {
		select {
		case <-wp.ctx.Done():
			return
		default:
		}

		writeStart := time.Now()
		var err error
		if wp.useUpsert {
			err = writeChunkUpsertWithWriter(wp.ctx, wp.tgtPool, wp.targetSchema, wp.targetTable,
				wp.targetCols, wp.colTypes, wp.colSRIDs, wp.targetPKCols, job.rows, writerID, wp.partitionID)
		} else {
			err = writeChunkGeneric(wp.ctx, wp.tgtPool, wp.targetSchema, wp.targetTable, wp.targetCols, job.rows)
		}

		if err != nil {
			wp.writeErr.CompareAndSwap(nil, &err)
			wp.cancel()
			return
		}

		writeDuration := time.Since(writeStart)
		atomic.AddInt64(&wp.totalWriteTime, int64(writeDuration))

		rowCount := int64(len(job.rows))
		atomic.AddInt64(&wp.totalWritten, rowCount)
		wp.prog.Add(rowCount)

		if wp.ackChan != nil {
			// Non-blocking send with context check to prevent deadlock.
			// If ackChan is full, we skip the ack rather than blocking the writer.
			// This is safe because checkpoint coordination handles out-of-order and
			// missing acks gracefully - the checkpoint just won't advance past this point.
			select {
			case wp.ackChan <- writeAck{
				readerID: job.readerID,
				seq:      job.seq,
				lastPK:   job.lastPK,
				rowNum:   job.rowNum,
			}:
				// Ack sent successfully
			case <-wp.ctx.Done():
				// Context cancelled, exit worker
				return
			default:
				// Channel full, skip this ack (checkpoint won't advance)
				// This should be rare with the large buffer, but prevents deadlock
			}
		}
	}
}

// submit sends a write job to the pool. Returns false if context is cancelled.
func (wp *writerPool) submit(job writeJob) bool {
	select {
	case wp.jobChan <- job:
		return true
	case <-wp.ctx.Done():
		return false
	}
}

// wait closes the job channel and waits for all workers to complete.
func (wp *writerPool) wait() {
	close(wp.jobChan)
	wp.writerWg.Wait()
	if wp.ackChan != nil {
		close(wp.ackChan)
		wp.ackWg.Wait()
	}
}

// error returns any write error that occurred.
func (wp *writerPool) error() error {
	if err := wp.writeErr.Load(); err != nil {
		return *err
	}
	return nil
}

// writeTime returns the total time spent writing.
func (wp *writerPool) writeTime() time.Duration {
	return time.Duration(atomic.LoadInt64(&wp.totalWriteTime))
}

// written returns the total rows written.
func (wp *writerPool) written() int64 {
	return atomic.LoadInt64(&wp.totalWritten)
}

// acks returns the ack channel for checkpoint coordination.
func (wp *writerPool) acks() <-chan writeAck {
	return wp.ackChan
}

// startAckProcessor starts a goroutine to process acks with the given handler.
func (wp *writerPool) startAckProcessor(handler func(writeAck)) {
	if wp.ackChan == nil {
		return
	}
	wp.ackWg.Add(1)
	go func() {
		defer wp.ackWg.Done()
		for ack := range wp.ackChan {
			handler(ack)
		}
	}()
}

// buildTargetPKCols sanitizes PK columns for the target database.
func buildTargetPKCols(pkCols []string, tgtPool pool.TargetPool) []string {
	isPGTarget := tgtPool.DBType() == "postgres"
	targetPKCols := make([]string, len(pkCols))
	for i, pk := range pkCols {
		if isPGTarget {
			targetPKCols[i] = target.SanitizePGIdentifier(pk)
		} else {
			targetPKCols[i] = pk
		}
	}
	return targetPKCols
}
