package storage

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/suite"
)

type CatalogSuite struct {
	suite.Suite
	f    *os.File
	cyc  *JournalCycle
	cycH *Header
}

func (s *CatalogSuite) SetupTest() {
	s.cyc, s.cycH = nil, nil // the suite instance is reused across tests; a prior test's committed cycle must not leak in

	path := filepath.Join(s.T().TempDir(), "test.db")
	f, err := os.Create(path)
	s.Require().NoError(err)
	s.f = f
}

func (s *CatalogSuite) TearDownTest() {
	if s.cyc != nil {
		_ = s.cyc.Commit(context.Background(), s.f, s.cycH)
	}
	s.Require().NoError(s.f.Close())
}

// cycle returns this test's single JournalCycle, opened against h on first
// call and reused after — one cycle per test, committed automatically
// (best-effort) in TearDownTest.
func (s *CatalogSuite) cycle(h *Header) *JournalCycle {
	if s.cyc == nil {
		c, err := NewJournalCycle(context.Background(), s.f, h)
		s.Require().NoError(err)
		s.cyc, s.cycH = c, h
	}
	return s.cyc
}

func TestCatalog(t *testing.T) {
	suite.Run(t, new(CatalogSuite))
}

func (s *CatalogSuite) TestCatalogPageHeader_EncodeDecode_RoundTrip() {
	buf := make([]byte, 64)
	encodeCatalogPageHeader(buf, 42, 7)

	next, occupied, err := decodeCatalogPageHeader(buf)
	s.Require().NoError(err)
	s.Equal(uint32(42), next)
	s.Equal(uint16(7), occupied)
}

func (s *CatalogSuite) TestDecodeCatalogPageHeader_RejectsWrongPageType() {
	buf := make([]byte, 64)
	buf[0] = byte(PageTypeFree)
	_, _, err := decodeCatalogPageHeader(buf)
	s.ErrorIs(err, ErrNotACatalogPage)
}

func (s *CatalogSuite) TestDecodeCatalogPageHeader_RejectsShortBuffer() {
	_, _, err := decodeCatalogPageHeader(make([]byte, catalogPageHeaderSize-1))
	s.ErrorIs(err, ErrShortPageRead)
}

func (s *CatalogSuite) TestCatalogCapacity_ComputedFromPageSize() {
	tests := []struct {
		pageSize uint32
		want     uint32
	}{
		{200, 2},                // (200-7)/85 = 2
		{177, 2},                // exact fit: (177-7)/85 = 2
		{4096, (4096 - 7) / 85}, // default page size
		{MaxPageSize, (MaxPageSize - 7) / 85},
	}
	for _, tt := range tests {
		got, err := catalogCapacity(tt.pageSize)
		s.Require().NoError(err)
		s.Equal(tt.want, got)
	}
}

func (s *CatalogSuite) TestCatalogCapacity_RejectsPageTooSmallForOneSlot() {
	// MinPageSize (64) < catalogPageHeaderSize(7) + collectionSlotSize(85) = 92
	tests := []uint32{MinPageSize, 91, catalogPageHeaderSize}
	for _, ps := range tests {
		_, err := catalogCapacity(ps)
		s.ErrorIsf(err, ErrCatalogPageTooSmall, "pageSize=%d", ps)
	}
}

func (s *CatalogSuite) TestReadCatalogPage_DetectsOccupiedCountExceedingCapacity() {
	ctx := context.Background()
	pageSize := uint32(200) // capacity 2
	h, err := NewHeader(pageSize)
	s.Require().NoError(err)
	s.Require().NoError(WriteHeader(ctx, s.f, h, s.cycle(h)))

	// Craft a catalog page whose occupied count (corrupted) exceeds what
	// this page size can actually hold, and verify it's rejected cleanly
	// instead of letting later slot-index arithmetic run past the buffer.
	buf := make([]byte, pageSize)
	encodeCatalogPageHeader(buf, 0, 3) // capacity is 2, claim 3
	s.Require().NoError(WritePage(ctx, s.f, h, s.cycle(h), 1, buf))

	_, err = readCatalogPage(ctx, s.f, h, 1)
	s.ErrorIs(err, ErrCorruptedCatalogPage)
}

func (s *CatalogSuite) TestReadCatalogPage_MaxUint16OccupiedDoesNotPanic() {
	// Adversarial: occupied count corrupted to the max representable
	// uint16, on a page far too small to hold that many slots. Must error,
	// never panic on out-of-bounds slot arithmetic.
	ctx := context.Background()
	pageSize := uint32(200)
	h, err := NewHeader(pageSize)
	s.Require().NoError(err)
	s.Require().NoError(WriteHeader(ctx, s.f, h, s.cycle(h)))

	buf := make([]byte, pageSize)
	encodeCatalogPageHeader(buf, 0, 65535)
	s.Require().NoError(WritePage(ctx, s.f, h, s.cycle(h), 1, buf))

	s.NotPanics(func() {
		_, err := readCatalogPage(ctx, s.f, h, 1)
		s.ErrorIs(err, ErrCorruptedCatalogPage)
	})
}

func (s *CatalogSuite) TestBootstrap_ProducesValidEmptyCatalogAnchor() {
	ctx := context.Background()
	h, err := Bootstrap(ctx, s.f, 200)
	s.Require().NoError(err)
	s.Equal(uint32(1), h.CatalogHead) // anchor is page 1 on a fresh file
	s.Equal(uint32(1), h.PageCount)

	view, err := readCatalogPage(ctx, s.f, h, h.CatalogHead)
	s.Require().NoError(err)
	s.Equal(uint32(0), view.next)
	s.Equal(uint16(0), view.occupied)

	entries, err := ListCollectionSlots(ctx, s.f, h)
	s.Require().NoError(err)
	s.Empty(entries)
}

func (s *CatalogSuite) TestBootstrap_HeaderPersistedToDisk() {
	ctx := context.Background()
	h, err := Bootstrap(ctx, s.f, 200)
	s.Require().NoError(err)

	reread, err := ReadHeader(ctx, s.f)
	s.Require().NoError(err)
	s.Equal(h.CatalogHead, reread.CatalogHead)
	s.Equal(h.PageCount, reread.PageCount)
	s.False(reread.Dirty) // Bootstrap commits its own cycle before returning
}

func (s *CatalogSuite) TestBootstrap_RejectsPageSizeTooSmallForCatalog() {
	_, err := Bootstrap(context.Background(), s.f, MinPageSize)
	s.ErrorIs(err, ErrCatalogPageTooSmall)
}

func (s *CatalogSuite) TestBootstrap_DefaultPageSize() {
	h, err := Bootstrap(context.Background(), s.f, 0)
	s.Require().NoError(err)
	s.Equal(DefaultPageSize, h.PageSize)
	s.Equal(uint32(1), h.CatalogHead)
}
