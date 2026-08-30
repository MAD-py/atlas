package storage

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/MAD-py/atlas/internal/file"
)

type JournalCycleSuite struct {
	suite.Suite
	f *os.File
	h *Header
}

func (s *JournalCycleSuite) SetupTest() {
	path := filepath.Join(s.T().TempDir(), "test.db")
	f, err := os.Create(path)
	s.Require().NoError(err)
	s.f = f

	h, err := Bootstrap(context.Background(), f, smallDataPageSize)
	s.Require().NoError(err)
	s.h = h
}

func (s *JournalCycleSuite) TearDownTest() {
	s.Require().NoError(s.f.Close())
}

func TestJournalCycle(t *testing.T) {
	suite.Run(t, new(JournalCycleSuite))
}

func (s *JournalCycleSuite) TestNewJournalCycle_CreatesJournalFileWithZeroRecords() {
	ctx := context.Background()
	cyc, err := NewJournalCycle(ctx, s.f, s.h)
	s.Require().NoError(err)

	info, err := os.Stat(file.JournalPathFor(s.f.Name()))
	s.Require().NoError(err)
	s.Equal(int64(journalHeaderSize), info.Size())
	s.Equal(uint32(0), cyc.recordCount)

	s.Require().NoError(cyc.Commit(ctx, s.f, s.h))
}

func (s *JournalCycleSuite) TestSnapshotIfNeeded_SkipsPageBeyondPhysicalExtent() {
	// Gap 1: a page not yet on disk (Allocate's grow-file path) has nothing
	// to snapshot — no journal record should be written for it.
	ctx := context.Background()
	cyc, err := NewJournalCycle(ctx, s.f, s.h)
	s.Require().NoError(err)

	newPage, err := Allocate(ctx, s.f, s.h, cyc)
	s.Require().NoError(err)
	s.Greater(newPage, s.h.PageCount-1)

	// Only page 0 (forced dirty on the cycle's first write) should have a
	// journal record — the newly grown page had nothing to snapshot.
	s.Equal(uint32(1), cyc.recordCount)
	_, snappedNewPage := cyc.snapshotted[newPage]
	s.True(snappedNewPage) // marked snapshotted (so a 2nd touch isn't re-attempted)...
	// ...but as "nothing to restore", not backed by an actual journal record.

	s.Require().NoError(cyc.Commit(ctx, s.f, s.h))
}

func (s *JournalCycleSuite) TestSnapshotIfNeeded_OnlySnapshotsFirstTouch() {
	ctx := context.Background()
	cyc, err := NewJournalCycle(ctx, s.f, s.h)
	s.Require().NoError(err)

	page, err := Allocate(ctx, s.f, s.h, cyc)
	s.Require().NoError(err)
	countAfterAlloc := cyc.recordCount

	// Write to the same page again within the same cycle.
	buf := make([]byte, s.h.PageSize)
	buf[0] = byte(PageTypeFree)
	s.Require().NoError(WritePage(ctx, s.f, s.h, cyc, page, buf))

	s.Equal(countAfterAlloc, cyc.recordCount) // no new record for the 2nd touch

	s.Require().NoError(cyc.Commit(ctx, s.f, s.h))
}

func (s *JournalCycleSuite) TestEnsureStarted_ForcesDirtyOnFirstWriteRegardlessOfPage() {
	ctx := context.Background()
	cyc, err := NewJournalCycle(ctx, s.f, s.h)
	s.Require().NoError(err)

	_, err = Allocate(ctx, s.f, s.h, cyc) // never directly touches WriteHeader with Dirty explicitly...
	s.Require().NoError(err)

	onDisk, err := ReadHeader(ctx, s.f)
	s.Require().NoError(err)
	s.True(onDisk.Dirty)
	s.True(s.h.Dirty) // the in-memory header mirrors it too

	s.Require().NoError(cyc.Commit(ctx, s.f, s.h))

	onDisk, err = ReadHeader(ctx, s.f)
	s.Require().NoError(err)
	s.False(onDisk.Dirty)
}

func (s *JournalCycleSuite) TestCommit_PersistsChangesAndDeletesJournal() {
	ctx := context.Background()
	cyc, err := NewJournalCycle(ctx, s.f, s.h)
	s.Require().NoError(err)

	page, err := Allocate(ctx, s.f, s.h, cyc)
	s.Require().NoError(err)

	s.Require().NoError(cyc.Commit(ctx, s.f, s.h))

	_, err = os.Stat(file.JournalPathFor(s.f.Name()))
	s.True(os.IsNotExist(err))

	onDisk, err := ReadHeader(ctx, s.f)
	s.Require().NoError(err)
	s.False(onDisk.Dirty)
	s.Equal(s.h.PageCount, onDisk.PageCount)
	s.GreaterOrEqual(page, uint32(1))
}

func (s *JournalCycleSuite) TestCommit_RejectsSecondCallOnSameCycle() {
	ctx := context.Background()
	cyc, err := NewJournalCycle(ctx, s.f, s.h)
	s.Require().NoError(err)
	s.Require().NoError(cyc.Commit(ctx, s.f, s.h))

	err = cyc.Commit(ctx, s.f, s.h)
	s.ErrorIs(err, ErrJournalCycleClosed)
}

// TestWrite_RejectsUseOfAlreadyClosedCycle covers the guard on the actual
// write path (snapshotIfNeeded's own done check), not just a second Commit
// call on Commit itself — a real caller reusing an already-closed cycle for
// a new write must be rejected the same way.
func (s *JournalCycleSuite) TestWrite_RejectsUseOfAlreadyClosedCycle() {
	ctx := context.Background()
	cyc, err := NewJournalCycle(ctx, s.f, s.h)
	s.Require().NoError(err)
	s.Require().NoError(cyc.Commit(ctx, s.f, s.h))

	_, err = Allocate(ctx, s.f, s.h, cyc)
	s.ErrorIs(err, ErrJournalCycleClosed)
}

func (s *JournalCycleSuite) TestAbort_RestoresOriginalPageContent() {
	ctx := context.Background()
	before, err := ReadPage(ctx, s.f, s.h.PageSize, s.h.CatalogHead)
	s.Require().NoError(err)
	beforeHeader := *s.h

	cyc, err := NewJournalCycle(ctx, s.f, s.h)
	s.Require().NoError(err)

	mutated := make([]byte, s.h.PageSize)
	copy(mutated, before)
	mutated[6] ^= 0xFF // corrupt something harmless past the fixed header fields
	s.Require().NoError(WritePage(ctx, s.f, s.h, cyc, s.h.CatalogHead, mutated))

	s.Require().NoError(cyc.Abort(ctx, s.f, s.h))

	after, err := ReadPage(ctx, s.f, s.h.PageSize, s.h.CatalogHead)
	s.Require().NoError(err)
	s.Equal(before, after)
	s.Equal(beforeHeader, *s.h)

	_, err = os.Stat(file.JournalPathFor(s.f.Name()))
	s.True(os.IsNotExist(err))
}

func (s *JournalCycleSuite) TestAbort_TruncatesPagesAllocatedThisCycle() {
	ctx := context.Background()
	beforeSize, err := s.f.Stat()
	s.Require().NoError(err)
	beforeHeader := *s.h

	cyc, err := NewJournalCycle(ctx, s.f, s.h)
	s.Require().NoError(err)
	_, err = Allocate(ctx, s.f, s.h, cyc)
	s.Require().NoError(err)

	grownSize, err := s.f.Stat()
	s.Require().NoError(err)
	s.Greater(grownSize.Size(), beforeSize.Size())

	s.Require().NoError(cyc.Abort(ctx, s.f, s.h))

	afterSize, err := s.f.Stat()
	s.Require().NoError(err)
	s.Equal(beforeSize.Size(), afterSize.Size())
	s.Equal(beforeHeader, *s.h)
}

func (s *JournalCycleSuite) TestAbort_NoOpWhenNothingWasEverTouched() {
	ctx := context.Background()
	cyc, err := NewJournalCycle(ctx, s.f, s.h)
	s.Require().NoError(err)

	s.Require().NoError(cyc.Abort(ctx, s.f, s.h))

	_, err = os.Stat(file.JournalPathFor(s.f.Name()))
	s.True(os.IsNotExist(err))
}

// TestAppendRecord_TornWriteLeavesRecordInvisible simulates a crash between
// a record's own bytes landing on disk and the journal header's count being
// bumped to acknowledge it: the record's bytes are appended directly (not
// through appendRecord, which would also bump the count), so the header
// still claims 0 records. Replay must treat that dangling record as if it
// doesn't exist, not read it as garbage.
func (s *JournalCycleSuite) TestAppendRecord_TornWriteLeavesRecordInvisible() {
	ctx := context.Background()
	cyc, err := NewJournalCycle(ctx, s.f, s.h)
	s.Require().NoError(err)

	rec := encodeJournalRecord(s.h.CatalogHead, make([]byte, s.h.PageSize))
	_, err = cyc.file.WriteAt(rec, journalHeaderSize)
	s.Require().NoError(err)
	// Deliberately do not bump the journal header's record count.

	touchedZero, err := replayJournalRecords(s.f, cyc.file, s.h.PageSize)
	s.Require().NoError(err)
	s.False(touchedZero) // the dangling record was never read

	s.Require().NoError(cyc.file.Close())
}

func (s *JournalCycleSuite) TestReplayJournalRecords_DetectsCorruptedRecord() {
	ctx := context.Background()
	cyc, err := NewJournalCycle(ctx, s.f, s.h)
	s.Require().NoError(err)
	s.Require().NoError(cyc.appendRecord(s.h.CatalogHead, make([]byte, s.h.PageSize)))

	// Corrupt one byte of the record's content, past its checksum-covered
	// page_number, directly on the journal file.
	_, err = cyc.file.WriteAt([]byte{0xFF}, journalHeaderSize+journalRecordPageNumSize+2)
	s.Require().NoError(err)

	_, err = replayJournalRecords(s.f, cyc.file, s.h.PageSize)
	s.ErrorIs(err, ErrCorruptedJournalRecord)

	s.Require().NoError(cyc.file.Close())
}
