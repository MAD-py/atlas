package storage

import (
	"context"
	"fmt"
	"os"

	"github.com/MAD-py/atlas/internal/file"
)

// JournalCycle is one MVP write operation's rollback-journal session: every
// page it's about to modify is snapshotted here, on first touch, before the
// real write lands. Every mutating internal/storage function takes one as a
// required parameter — never nil-checked, never defaulted — since deciding
// whether an operation gets its own fresh cycle or shares one with others
// belongs to whoever is orchestrating a sequence of operations, not to a
// single page-level write.
type JournalCycle struct {
	file *os.File

	pageSize    uint32
	recordCount uint32
	snapshotted map[uint32]struct{}

	// forceDirtyOnce guards the gap-2 fix: the on-disk dirty byte must be
	// forced to 1 on the very first write of the whole cycle, regardless of
	// which page triggers it, since an operation that never naturally
	// touches the header (a tombstone-only delete) must still mark the
	// cycle in-progress the moment it starts.
	forceDirtyOnce bool
	done           bool
}

// NewJournalCycle creates (truncating any stale leftover) the .journal file
// next to f and writes its initial header (record count 0).
func NewJournalCycle(ctx context.Context, f *os.File, h *Header) (*JournalCycle, error) {
	jf, err := file.CreateJournal(f)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrJournalOpenFailed, err)
	}
	if err := writeJournalHeader(jf, 0); err != nil {
		jf.Close()
		return nil, err
	}
	return &JournalCycle{
		file:        jf,
		pageSize:    h.PageSize,
		snapshotted: make(map[uint32]struct{}),
	}, nil
}

// appendRecord writes one journal record and fsyncs it, then bumps and
// fsyncs the journal header's record count — never the reverse, so a crash
// mid-append only ever leaves the header under-counting a half-written
// record, never claiming one that isn't actually complete.
func (c *JournalCycle) appendRecord(pageNum uint32, content []byte) error {
	rec := encodeJournalRecord(pageNum, content)
	off := journalHeaderSize + int64(c.recordCount)*journalRecordSize(c.pageSize)
	if _, err := c.file.WriteAt(rec, off); err != nil {
		return fmt.Errorf("%w: %v", ErrJournalWriteFailed, err)
	}
	if err := c.file.Sync(); err != nil {
		return fmt.Errorf("%w: %v", ErrJournalWriteFailed, err)
	}
	c.recordCount++
	return writeJournalHeader(c.file, c.recordCount)
}

// snapshotIfNeeded journals pageNum's current on-disk content the first
// time it's touched this cycle, per the incremental/lazy snapshotting model
// — the journal must hold the state from before the cycle started, so a
// page touched more than once (e.g. tombstoned then immediately freed) is
// only ever snapshotted on that first touch.
func (c *JournalCycle) snapshotIfNeeded(ctx context.Context, f *os.File, h *Header, pageNum uint32) error {
	if c.done {
		return ErrJournalCycleClosed
	}
	if _, done := c.snapshotted[pageNum]; done {
		return nil
	}
	exists, err := pageExistsOnDisk(f, h.PageSize, pageNum)
	if err != nil {
		return err
	}
	if exists {
		content, err := ReadPage(ctx, f, h.PageSize, pageNum)
		if err != nil {
			return err
		}
		if err := c.appendRecord(pageNum, content); err != nil {
			return err
		}
	}
	c.snapshotted[pageNum] = struct{}{}
	return nil
}

// ensureStarted forces the on-disk dirty byte to 1 the first time it's
// called this cycle — gap 2 from the design review — so the flag can't be
// silently left clean by code (Allocate/Free's own header rewrites) that has
// no idea a cycle is active. A no-op on every call after the first. Commit's
// own final write bypasses this entirely via commitHeader, since it's the
// one write allowed to persist dirty=false.
func (c *JournalCycle) ensureStarted(ctx context.Context, f *os.File, h *Header) error {
	if c.forceDirtyOnce {
		return nil
	}
	if err := c.snapshotIfNeeded(ctx, f, h, 0); err != nil {
		return err
	}
	forced := *h
	forced.Dirty = true
	buf := make([]byte, h.PageSize)
	copy(buf, forced.Encode())
	if err := writePageRaw(f, h.PageSize, 0, buf); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("%w: %v", ErrPageWriteFailed, err)
	}
	h.Dirty = true
	c.forceDirtyOnce = true
	return nil
}

// commitHeader writes h as page 0 without going through ensureStarted —
// the bypass Commit needs, since forcing dirty=1 right before Commit means
// to persist dirty=false would corrupt the very state Commit is trying to
// write. Still snapshots page 0 first if this cycle never touched it.
func (c *JournalCycle) commitHeader(ctx context.Context, f *os.File, h *Header) error {
	if c.done {
		return ErrJournalCycleClosed
	}
	if err := c.snapshotIfNeeded(ctx, f, h, 0); err != nil {
		return err
	}
	buf := make([]byte, h.PageSize)
	copy(buf, h.Encode())
	return writePageRaw(f, h.PageSize, 0, buf)
}

// Commit persists h (Dirty forced false) as the final page-0 state, fsyncs
// the .db, then deletes the journal — steps 3-4 of the commit protocol.
func (c *JournalCycle) Commit(ctx context.Context, f *os.File, h *Header) error {
	if c.done {
		return ErrJournalCycleClosed
	}
	final := *h
	final.Dirty = false
	if err := c.commitHeader(ctx, f, &final); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("%w: %v", ErrPageWriteFailed, err)
	}
	*h = final

	journalPath := c.file.Name()
	if err := c.file.Close(); err != nil {
		return fmt.Errorf("%w: %v", ErrJournalWriteFailed, err)
	}
	if err := os.Remove(journalPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("%w: %v", ErrJournalWriteFailed, err)
	}
	c.done = true
	return nil
}

// Abort undoes every write this cycle made so far, on demand — the same
// replay-and-truncate logic recovery runs at Open(), but usable mid-process
// so a non-crash error partway through an operation can be cleanly undone
// without restarting.
func (c *JournalCycle) Abort(ctx context.Context, f *os.File, h *Header) error {
	if c.done {
		return ErrJournalCycleClosed
	}
	touchedPageZero, err := replayJournalRecords(f, c.file, c.pageSize)
	if err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("%w: %v", ErrPageWriteFailed, err)
	}

	if touchedPageZero {
		restored, err := ReadHeader(ctx, f)
		if err != nil {
			return err
		}
		if err := truncateToPageCount(f, restored); err != nil {
			return err
		}
		if err := f.Sync(); err != nil {
			return fmt.Errorf("%w: %v", ErrPageWriteFailed, err)
		}
		*h = *restored
	}

	journalPath := c.file.Name()
	if err := c.file.Close(); err != nil {
		return fmt.Errorf("%w: %v", ErrJournalWriteFailed, err)
	}
	if err := os.Remove(journalPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("%w: %v", ErrJournalWriteFailed, err)
	}
	c.done = true
	return nil
}

func writeJournalHeader(f *os.File, recordCount uint32) error {
	if _, err := f.WriteAt(encodeJournalHeader(recordCount), 0); err != nil {
		return fmt.Errorf("%w: %v", ErrJournalWriteFailed, err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("%w: %v", ErrJournalWriteFailed, err)
	}
	return nil
}
