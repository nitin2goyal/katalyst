package store

import (
	"context"
	"database/sql"
	"log/slog"
	"sync"
	"sync/atomic"
)

// Writer is a channel-based async writer that serializes SQLite writes through
// a single goroutine. It provides backpressure via a bounded channel, ordered
// writes, and graceful drain on shutdown.
type Writer struct {
	db      *sql.DB
	ch      chan func(*sql.DB)
	wg      sync.WaitGroup
	dropped atomic.Uint64
}

// NewWriter creates an async writer with the given buffer size.
// Call Run() to start processing and Drain() before closing the DB.
func NewWriter(db *sql.DB, bufSize int) *Writer {
	if bufSize <= 0 {
		bufSize = 4096
	}
	return &Writer{
		db: db,
		ch: make(chan func(*sql.DB), bufSize),
	}
}

// Run processes queued writes until ctx is cancelled. After cancellation it
// drains remaining items in the channel before returning.
func (w *Writer) Run(ctx context.Context) {
	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		for {
			select {
			case fn, ok := <-w.ch:
				if !ok {
					return
				}
				fn(w.db)
			case <-ctx.Done():
				// Drain remaining items
				for {
					select {
					case fn, ok := <-w.ch:
						if !ok {
							return
						}
						fn(w.db)
					default:
						return
					}
				}
			}
		}
	}()
}

// Enqueue adds a write operation to the queue. If the channel is full, it
// drops the write (backpressure) rather than blocking the caller.
func (w *Writer) Enqueue(fn func(*sql.DB)) {
	select {
	case w.ch <- fn:
	default:
		count := w.dropped.Add(1)
		// Log a warning at powers of 2 to avoid log spam
		if count&(count-1) == 0 {
			slog.Warn("async writer: dropping writes due to backpressure",
				"totalDropped", count, "queueCap", cap(w.ch))
		}
	}
}

// DroppedCount returns the number of writes dropped due to backpressure.
func (w *Writer) DroppedCount() uint64 {
	return w.dropped.Load()
}

// Drain waits for all queued writes to be processed. Call this before
// closing the database.
func (w *Writer) Drain() {
	close(w.ch)
	w.wg.Wait()
}
