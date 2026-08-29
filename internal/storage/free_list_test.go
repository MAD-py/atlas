package storage

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/suite"
)

type FreeListSuite struct {
	suite.Suite
	f   *os.File
	h   *Header
	cyc *JournalCycle
}

func (s *FreeListSuite) SetupTest() {
	s.cyc = nil // the suite instance is reused across tests; a prior test's committed cycle must not leak in

	path := filepath.Join(s.T().TempDir(), "test.db")
	f, err := os.Create(path)
	s.Require().NoError(err)
	s.f = f

	h, err := NewHeader(256)
	s.Require().NoError(err)
	buf := make([]byte, h.PageSize)
	copy(buf, h.Encode())
	s.Require().NoError(writePageRaw(f, h.PageSize, 0, buf))
	s.h = h
}

func (s *FreeListSuite) TearDownTest() {
	if s.cyc != nil {
		_ = s.cyc.Commit(context.Background(), s.f, s.h)
	}
	s.Require().NoError(s.f.Close())
}

// cycle returns this test's single JournalCycle, opening one on first call
// and reusing it after — every mutating call in a test shares one cycle,
// committed automatically in TearDownTest.
func (s *FreeListSuite) cycle() *JournalCycle {
	if s.cyc == nil {
		c, err := NewJournalCycle(context.Background(), s.f, s.h)
		s.Require().NoError(err)
		s.cyc = c
	}
	return s.cyc
}

func TestFreeList(t *testing.T) {
	suite.Run(t, new(FreeListSuite))
}

func (s *FreeListSuite) TestAllocate_GrowsFileWhenFreeListEmpty() {
	ctx := context.Background()
	p1, err := Allocate(ctx, s.f, s.h, s.cycle())
	s.Require().NoError(err)
	s.Equal(uint32(1), p1)
	s.Equal(uint32(1), s.h.PageCount)
	s.Equal(uint32(0), s.h.FreeListHead)

	p2, err := Allocate(ctx, s.f, s.h, s.cycle())
	s.Require().NoError(err)
	s.Equal(uint32(2), p2)
	s.Equal(uint32(2), s.h.PageCount)
}

func (s *FreeListSuite) TestAllocateFree_ReusesFreedPageBeforeGrowing() {
	ctx := context.Background()
	_, err := Allocate(ctx, s.f, s.h, s.cycle())
	s.Require().NoError(err)
	p2, err := Allocate(ctx, s.f, s.h, s.cycle())
	s.Require().NoError(err)
	_, err = Allocate(ctx, s.f, s.h, s.cycle())
	s.Require().NoError(err)
	s.Equal(uint32(3), s.h.PageCount)

	s.Require().NoError(Free(ctx, s.f, s.h, s.cycle(), p2))
	s.Equal(p2, s.h.FreeListHead)

	got, err := Allocate(ctx, s.f, s.h, s.cycle())
	s.Require().NoError(err)
	s.Equal(p2, got)
	s.Equal(uint32(3), s.h.PageCount) // reused, not grown
	s.Equal(uint32(0), s.h.FreeListHead)

	got2, err := Allocate(ctx, s.f, s.h, s.cycle())
	s.Require().NoError(err)
	s.Equal(uint32(4), got2)
	s.Equal(uint32(4), s.h.PageCount)
}

func (s *FreeListSuite) TestFree_LIFOOrderAndHeadPointerStaysCorrect() {
	ctx := context.Background()
	p1, err := Allocate(ctx, s.f, s.h, s.cycle())
	s.Require().NoError(err)
	p2, err := Allocate(ctx, s.f, s.h, s.cycle())
	s.Require().NoError(err)
	p3, err := Allocate(ctx, s.f, s.h, s.cycle())
	s.Require().NoError(err)

	s.Require().NoError(Free(ctx, s.f, s.h, s.cycle(), p1))
	s.Equal(p1, s.h.FreeListHead)
	s.Require().NoError(Free(ctx, s.f, s.h, s.cycle(), p2))
	s.Equal(p2, s.h.FreeListHead)
	s.Require().NoError(Free(ctx, s.f, s.h, s.cycle(), p3))
	s.Equal(p3, s.h.FreeListHead)

	got, err := Allocate(ctx, s.f, s.h, s.cycle())
	s.Require().NoError(err)
	s.Equal(p3, got)
	s.Equal(p2, s.h.FreeListHead)

	got, err = Allocate(ctx, s.f, s.h, s.cycle())
	s.Require().NoError(err)
	s.Equal(p2, got)
	s.Equal(p1, s.h.FreeListHead)

	got, err = Allocate(ctx, s.f, s.h, s.cycle())
	s.Require().NoError(err)
	s.Equal(p1, got)
	s.Equal(uint32(0), s.h.FreeListHead)
}

func (s *FreeListSuite) TestFree_RejectsInvalidPageNumber() {
	ctx := context.Background()
	p1, err := Allocate(ctx, s.f, s.h, s.cycle())
	s.Require().NoError(err)

	tests := []uint32{0, p1 + 1, math.MaxUint32}
	for _, pn := range tests {
		s.ErrorIs(Free(ctx, s.f, s.h, s.cycle(), pn), ErrInvalidPageNumber)
	}
}

func (s *FreeListSuite) TestAllocate_PageCountOverflow() {
	s.h.PageCount = math.MaxUint32
	_, err := Allocate(context.Background(), s.f, s.h, s.cycle())
	s.ErrorIs(err, ErrPageCountOverflow)
}

func (s *FreeListSuite) TestAllocate_DetectsCorruptedFreeListHead() {
	ctx := context.Background()
	p1, err := Allocate(ctx, s.f, s.h, s.cycle()) // grown, blank content, never freed
	s.Require().NoError(err)

	s.h.FreeListHead = p1 // corrupt: point the free list at a non-free page
	_, err = Allocate(ctx, s.f, s.h, s.cycle())
	s.ErrorIs(err, ErrNotAFreePage)
}

func (s *FreeListSuite) TestAllocate_HeaderPersistedToDisk() {
	ctx := context.Background()
	_, err := Allocate(ctx, s.f, s.h, s.cycle())
	s.Require().NoError(err)

	reread, err := ReadHeader(ctx, s.f)
	s.Require().NoError(err)
	s.Equal(s.h.PageCount, reread.PageCount)
	s.Equal(s.h.FreeListHead, reread.FreeListHead)
}
