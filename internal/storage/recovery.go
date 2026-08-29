package storage

import (
	"context"
	"fmt"
	"math"
	"os"
)

// truncateToPageCount discards anything past (h.PageCount+1)*h.PageSize —
// used by both recovery and Abort to close gap 1: since page numbers are
// assigned sequentially, this can only ever discard pages the cycle being
// undone appended, never a page any prior, already-committed state depended
// on.
func truncateToPageCount(f *os.File, h *Header) error {
	want := (uint64(h.PageCount) + 1) * uint64(h.PageSize)
	if want > math.MaxInt64 {
		return ErrPageOffsetOverflow
	}
	return f.Truncate(int64(want))
}

// replayJournalRecords writes every record in journal (read from its start)
// back to its page_number in f. touchedPageZero reports whether page 0 was
// among them — this package's cycle mechanism always snapshots page 0 as
// its very first action (see JournalCycle.ensureStarted), so a journal
// missing that record can't actually explain the state it's meant to
// recover from.
func replayJournalRecords(f, journal *os.File, pageSize uint32) (touchedPageZero bool, err error) {
	buf := make([]byte, journalHeaderSize)
	n, err := journal.ReadAt(buf, 0)
	if err != nil || n != journalHeaderSize {
		return false, fmt.Errorf("%w: %v", ErrTruncatedJournalHeader, err)
	}
	count, err := decodeJournalHeader(buf)
	if err != nil {
		return false, err
	}

	recSize := journalRecordSize(pageSize)
	recBuf := make([]byte, recSize)
	for i := range count {
		off := journalHeaderSize + int64(i)*recSize
		n, err := journal.ReadAt(recBuf, off)
		if err != nil || int64(n) != recSize {
			return false, fmt.Errorf("%w: record %d: %v", ErrShortJournalRecord, i, err)
		}
		pageNum, content, err := decodeJournalRecord(recBuf, pageSize)
		if err != nil {
			return false, fmt.Errorf("%w: record %d", err, i)
		}
		if err := writePageRaw(f, pageSize, pageNum, content); err != nil {
			return false, err
		}
		if pageNum == 0 {
			touchedPageZero = true
		}
	}
	return touchedPageZero, nil
}

// RecoverIfNeeded implements the four-state recovery table crash safety
// depends on, meant to be called by a future Open() before anything else
// touches the file. It bypasses journaling entirely — recovery runs before
// any JournalCycle exists.
func RecoverIfNeeded(ctx context.Context, f *os.File) (*Header, error) {
	h, err := ReadHeader(ctx, f)
	if err != nil {
		return nil, err
	}

	journalPath := journalPathFor(f.Name())
	journal, openErr := os.OpenFile(journalPath, os.O_RDONLY, 0)
	journalExists := openErr == nil
	if openErr != nil && !os.IsNotExist(openErr) {
		return nil, fmt.Errorf("%w: %v", ErrJournalOpenFailed, openErr)
	}

	switch {
	// Normal: no crash in progress, nothing to recover.
	case !h.Dirty && !journalExists:
		return h, nil

	// Crash between the commit protocol's step 2 and step 3: replay the
	// journal to roll back to the state before the interrupted cycle.
	case h.Dirty && journalExists:
		restored, replayErr := replayJournalIntoHeader(ctx, f, journal, h.PageSize)
		closeErr := journal.Close()
		if replayErr != nil {
			return nil, fmt.Errorf("%w: %v", ErrJournalMissing, replayErr)
		}
		if closeErr != nil {
			return nil, fmt.Errorf("%w: %v", ErrJournalWriteFailed, closeErr)
		}
		if err := os.Remove(journalPath); err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: %v", ErrJournalWriteFailed, err)
		}
		return restored, nil

	// Not explainable by crash timing alone (no journal to replay, yet the
	// header claims a cycle was in progress) — refuse rather than guess.
	case h.Dirty && !journalExists:
		return nil, fmt.Errorf("%w: %s", ErrJournalMissing, journalPath)

	// Orphaned journal, harmless: crash before step 2 (nothing touched yet)
	// or after step 4 (already fully committed) of some prior cycle — clean
	// it up and open normally.
	default: // !h.Dirty && journalExists
		if err := journal.Close(); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrJournalWriteFailed, err)
		}
		if err := os.Remove(journalPath); err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: %v", ErrJournalWriteFailed, err)
		}
		return h, nil
	}
}

// replayJournalIntoHeader replays journal into f, then truncates away
// anything the crashed cycle appended past the header's restored page
// count, and returns that restored header (already Dirty=false — the
// snapshot of page 0 was taken before the cycle forced it to true).
func replayJournalIntoHeader(ctx context.Context, f, journal *os.File, pageSize uint32) (*Header, error) {
	touchedPageZero, err := replayJournalRecords(f, journal, pageSize)
	if err != nil {
		return nil, err
	}
	if !touchedPageZero {
		return nil, fmt.Errorf("%w: journal has no header record", ErrCorruptedJournalRecord)
	}
	if err := f.Sync(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrPageWriteFailed, err)
	}

	restored, err := ReadHeader(ctx, f)
	if err != nil {
		return nil, err
	}
	if err := truncateToPageCount(f, restored); err != nil {
		return nil, err
	}
	if err := f.Sync(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrPageWriteFailed, err)
	}
	return restored, nil
}
