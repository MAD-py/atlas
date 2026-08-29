package storage

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/suite"
)

type RecoverySuite struct {
	suite.Suite
	f *os.File
	h *Header
}

func (s *RecoverySuite) SetupTest() {
	path := filepath.Join(s.T().TempDir(), "test.db")
	f, err := os.Create(path)
	s.Require().NoError(err)
	s.f = f

	h, err := Bootstrap(context.Background(), f, smallDataPageSize)
	s.Require().NoError(err)
	s.h = h
}

func (s *RecoverySuite) TearDownTest() {
	s.Require().NoError(s.f.Close())
}

func TestRecovery(t *testing.T) {
	suite.Run(t, new(RecoverySuite))
}

// forceDirtyBit writes the dirty byte directly, bypassing any JournalCycle
// — simulating a header state left by processes other than this package's
// own cycle mechanism (e.g. a hand-corrupted or externally tampered file).
func (s *RecoverySuite) forceDirtyBit(dirty bool) {
	forced := *s.h
	forced.Dirty = dirty
	buf := make([]byte, s.h.PageSize)
	copy(buf, forced.Encode())
	s.Require().NoError(writePageRaw(s.f, s.h.PageSize, 0, buf))
}

// --- the 4-state table ---

func (s *RecoverySuite) TestRecoverIfNeeded_NormalState_NoOp() {
	recovered, err := RecoverIfNeeded(context.Background(), s.f)
	s.Require().NoError(err)
	s.Equal(s.h.PageCount, recovered.PageCount)
	s.False(recovered.Dirty)
}

func (s *RecoverySuite) TestRecoverIfNeeded_OrphanedJournal_CleansUpAndOpens() {
	// dirty=false but a stray journal exists — harmless (crash before step 2,
	// or after step 4, of some prior cycle).
	journalPath := journalPathFor(s.f.Name())
	s.Require().NoError(os.WriteFile(journalPath, encodeJournalHeader(0), 0o600))

	recovered, err := RecoverIfNeeded(context.Background(), s.f)
	s.Require().NoError(err)
	s.False(recovered.Dirty)

	_, err = os.Stat(journalPath)
	s.True(os.IsNotExist(err))
}

func (s *RecoverySuite) TestRecoverIfNeeded_DirtyNoJournal_Refuses() {
	s.forceDirtyBit(true)

	_, err := RecoverIfNeeded(context.Background(), s.f)
	s.ErrorIs(err, ErrJournalMissing)
}

func (s *RecoverySuite) TestRecoverIfNeeded_DirtyCorruptJournal_Refuses() {
	s.forceDirtyBit(true)
	journalPath := journalPathFor(s.f.Name())
	s.Require().NoError(os.WriteFile(journalPath, []byte("not a journal file"), 0o600))

	_, err := RecoverIfNeeded(context.Background(), s.f)
	s.ErrorIs(err, ErrJournalMissing)
}

func (s *RecoverySuite) TestRecoverIfNeeded_DirtyJournalMissingHeaderRecord_Refuses() {
	// A journal that never actually recorded page 0 can't explain how the
	// dirty bit got set in the first place — same "not explainable by crash
	// timing alone" bucket as a missing/corrupt journal.
	s.forceDirtyBit(true)
	ctx := context.Background()
	cyc, err := NewJournalCycle(ctx, s.f, s.h)
	s.Require().NoError(err)
	s.Require().NoError(cyc.appendRecord(s.h.CatalogHead, make([]byte, s.h.PageSize)))
	s.Require().NoError(cyc.file.Close())

	_, err = RecoverIfNeeded(ctx, s.f)
	s.ErrorIs(err, ErrJournalMissing)
}

// TestRecoverIfNeeded_DirtyTruncatedJournal_Refuses covers a journal whose
// header claims more records than the file actually holds — a shape distinct
// from bad magic/checksum (the header itself decodes fine) and from a
// missing page-0 record (the one record present is perfectly valid). Under
// this package's own append ordering (write the record, then bump the
// header's count) this can't happen from crash timing alone, only from
// external truncation/tampering after the fact — still must be refused, not
// misread as a short record or hang.
func (s *RecoverySuite) TestRecoverIfNeeded_DirtyTruncatedJournal_Refuses() {
	s.forceDirtyBit(true)
	ctx := context.Background()
	cyc, err := NewJournalCycle(ctx, s.f, s.h)
	s.Require().NoError(err)
	s.Require().NoError(cyc.appendRecord(0, make([]byte, s.h.PageSize))) // one real record (page 0)
	s.Require().NoError(writeJournalHeader(cyc.file, 2))                 // claim a 2nd that was never written
	s.Require().NoError(cyc.file.Close())

	_, err = RecoverIfNeeded(ctx, s.f)
	s.ErrorIs(err, ErrJournalMissing)
}

// --- full crash-recovery integrations ---

// TestRecovery_SinglePageWrite covers a crash mid-cycle where only
// already-existing pages were modified — no allocation involved.
func (s *RecoverySuite) TestRecovery_SinglePageWrite() {
	ctx := context.Background()

	setupCyc, err := NewJournalCycle(ctx, s.f, s.h)
	s.Require().NoError(err)
	ref, err := CreateCollectionSlot(ctx, s.f, s.h, setupCyc, "docs", false)
	s.Require().NoError(err)
	slot := CollectionSlot{Name: "docs"}
	_, _, err = InsertRecord(ctx, s.f, s.h, setupCyc, ref, &slot, makeRecord(1, 0))
	s.Require().NoError(err)
	_, _, err = InsertRecord(ctx, s.f, s.h, setupCyc, ref, &slot, makeRecord(2, 0))
	s.Require().NoError(err)
	s.Require().NoError(setupCyc.Commit(ctx, s.f, s.h))

	beforeHeader := *s.h
	beforeCatalog, err := ReadPage(ctx, s.f, s.h.PageSize, s.h.CatalogHead)
	s.Require().NoError(err)
	beforeData, err := ReadPage(ctx, s.f, s.h.PageSize, slot.Head)
	s.Require().NoError(err)
	beforeInfo, err := s.f.Stat()
	s.Require().NoError(err)

	crashCyc, err := NewJournalCycle(ctx, s.f, s.h)
	s.Require().NoError(err)
	s.Require().NoError(DeleteRecord(ctx, s.f, s.h, crashCyc, ref, &slot, recordID(2)))
	// Crash here: never call Commit or Abort on crashCyc.

	recovered, err := RecoverIfNeeded(ctx, s.f)
	s.Require().NoError(err)

	s.Equal(beforeHeader, *recovered)
	s.False(recovered.Dirty)

	gotCatalog, err := ReadPage(ctx, s.f, recovered.PageSize, recovered.CatalogHead)
	s.Require().NoError(err)
	s.Equal(beforeCatalog, gotCatalog)

	gotData, err := ReadPage(ctx, s.f, recovered.PageSize, slot.Head)
	s.Require().NoError(err)
	s.Equal(beforeData, gotData)

	afterInfo, err := s.f.Stat()
	s.Require().NoError(err)
	s.Equal(beforeInfo.Size(), afterInfo.Size())

	_, err = os.Stat(journalPathFor(s.f.Name()))
	s.True(os.IsNotExist(err))

	// Functional confirmation: the "deleted" record is back.
	rec, _, _, err := FindRecordByID(ctx, s.f, recovered, slot.Head, recordID(2))
	s.Require().NoError(err)
	s.Equal(byte(2), rec[0])
}

// TestRecovery_NewPageAllocation covers the scenario the design review
// specifically audited: an insert that allocates a new tail page mid-cycle.
// Recovery must not just restore the pages that existed before the cycle —
// it must also truncate away the page the crashed cycle appended (gap 1).
func (s *RecoverySuite) TestRecovery_NewPageAllocation() {
	ctx := context.Background()

	setupCyc, err := NewJournalCycle(ctx, s.f, s.h)
	s.Require().NoError(err)
	ref, err := CreateCollectionSlot(ctx, s.f, s.h, setupCyc, "docs", false)
	s.Require().NoError(err)
	slot := CollectionSlot{Name: "docs"}
	maxSize := dataPageMaxRecordSize(smallDataPageSize)
	_, _, err = InsertRecord(ctx, s.f, s.h, setupCyc, ref, &slot, makeRecord(1, int(maxSize)-recordIDSize))
	s.Require().NoError(err) // fills the first data page exactly
	s.Require().NoError(setupCyc.Commit(ctx, s.f, s.h))

	beforeHeader := *s.h
	beforeCatalog, err := ReadPage(ctx, s.f, s.h.PageSize, s.h.CatalogHead)
	s.Require().NoError(err)
	beforeData, err := ReadPage(ctx, s.f, s.h.PageSize, slot.Head)
	s.Require().NoError(err)
	beforeInfo, err := s.f.Stat()
	s.Require().NoError(err)

	crashCyc, err := NewJournalCycle(ctx, s.f, s.h)
	s.Require().NoError(err)
	_, _, err = InsertRecord(ctx, s.f, s.h, crashCyc, ref, &slot, makeRecord(2, 0)) // must allocate a new tail page
	s.Require().NoError(err)
	s.Require().Greater(s.h.PageCount, beforeHeader.PageCount)
	// Crash here: never call Commit or Abort on crashCyc.

	recovered, err := RecoverIfNeeded(ctx, s.f)
	s.Require().NoError(err)

	s.Equal(beforeHeader, *recovered)

	afterInfo, err := s.f.Stat()
	s.Require().NoError(err)
	s.Equal(beforeInfo.Size(), afterInfo.Size()) // the appended page is gone
	s.Equal((int64(recovered.PageCount)+1)*int64(recovered.PageSize), afterInfo.Size())

	gotCatalog, err := ReadPage(ctx, s.f, recovered.PageSize, recovered.CatalogHead)
	s.Require().NoError(err)
	s.Equal(beforeCatalog, gotCatalog)

	gotData, err := ReadPage(ctx, s.f, recovered.PageSize, slot.Head)
	s.Require().NoError(err)
	s.Equal(beforeData, gotData)

	_, err = os.Stat(journalPathFor(s.f.Name()))
	s.True(os.IsNotExist(err))

	// Functional confirmation: the never-committed 2nd record is gone, the
	// 1st (already committed before the crash) is untouched.
	_, _, _, err = FindRecordByID(ctx, s.f, recovered, slot.Head, recordID(2))
	s.ErrorIs(err, ErrDocumentNotFound)
	rec, _, _, err := FindRecordByID(ctx, s.f, recovered, slot.Head, recordID(1))
	s.Require().NoError(err)
	s.Equal(byte(1), rec[0])
}

// TestRecovery_SamePageWrittenTwice covers a data page tombstoned and then
// immediately freed within the same cycle — the page must be journaled
// only once, holding its state from before the cycle started, not the
// intermediate tombstoned-but-not-yet-freed state.
func (s *RecoverySuite) TestRecovery_SamePageWrittenTwice() {
	ctx := context.Background()

	setupCyc, err := NewJournalCycle(ctx, s.f, s.h)
	s.Require().NoError(err)
	ref, err := CreateCollectionSlot(ctx, s.f, s.h, setupCyc, "docs", false)
	s.Require().NoError(err)
	slot := CollectionSlot{Name: "docs"}
	_, _, err = InsertRecord(ctx, s.f, s.h, setupCyc, ref, &slot, makeRecord(1, 0))
	s.Require().NoError(err)
	s.Require().NoError(setupCyc.Commit(ctx, s.f, s.h))

	beforeHeader := *s.h
	beforeCatalog, err := ReadPage(ctx, s.f, s.h.PageSize, s.h.CatalogHead)
	s.Require().NoError(err)
	beforeData, err := ReadPage(ctx, s.f, s.h.PageSize, slot.Head)
	s.Require().NoError(err)
	dataPage := slot.Head

	crashCyc, err := NewJournalCycle(ctx, s.f, s.h)
	s.Require().NoError(err)
	// Deleting the only record tombstones dataPage, finds it now empty, and
	// immediately frees it — two separate writes to dataPage in one cycle.
	s.Require().NoError(DeleteRecord(ctx, s.f, s.h, crashCyc, ref, &slot, recordID(1)))
	s.Equal(dataPage, s.h.FreeListHead) // confirms the free-list push actually happened

	// The snapshot-once rule: 3 distinct pages touched this cycle (header,
	// catalog anchor, the data page) — never a 4th record for dataPage's
	// second write.
	s.Equal(uint32(3), crashCyc.recordCount)
	// Crash here: never call Commit or Abort on crashCyc.

	recovered, err := RecoverIfNeeded(ctx, s.f)
	s.Require().NoError(err)

	s.Equal(beforeHeader, *recovered)
	s.Equal(uint32(0), recovered.FreeListHead)

	gotCatalog, err := ReadPage(ctx, s.f, recovered.PageSize, recovered.CatalogHead)
	s.Require().NoError(err)
	s.Equal(beforeCatalog, gotCatalog)

	gotData, err := ReadPage(ctx, s.f, recovered.PageSize, dataPage)
	s.Require().NoError(err)
	s.Equal(beforeData, gotData)
	s.Equal(byte(PageTypeData), gotData[0]) // restored as a data page, not the free-list marker it became mid-cycle

	_, err = os.Stat(journalPathFor(s.f.Name()))
	s.True(os.IsNotExist(err))

	// Functional confirmation: the "deleted" record is back.
	rec, _, _, err := FindRecordByID(ctx, s.f, recovered, dataPage, recordID(1))
	s.Require().NoError(err)
	s.Equal(byte(1), rec[0])
}
