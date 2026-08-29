package storage

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/suite"
)

type PageSuite struct {
	suite.Suite
	f    *os.File
	cyc  *JournalCycle
	cycH *Header
}

func (s *PageSuite) SetupTest() {
	s.cyc, s.cycH = nil, nil // the suite instance is reused across tests; a prior test's committed cycle must not leak in

	path := filepath.Join(s.T().TempDir(), "test.db")
	f, err := os.Create(path)
	s.Require().NoError(err)
	s.f = f
}

func (s *PageSuite) TearDownTest() {
	if s.cyc != nil {
		_ = s.cyc.Commit(context.Background(), s.f, s.cycH)
	}
	s.Require().NoError(s.f.Close())
}

// cycle returns this test's single JournalCycle, opened against h on first
// call and reused after regardless of which h is passed subsequently — one
// cycle per test, committed automatically (best-effort) in TearDownTest.
func (s *PageSuite) cycle(h *Header) *JournalCycle {
	if s.cyc == nil {
		c, err := NewJournalCycle(context.Background(), s.f, h)
		s.Require().NoError(err)
		s.cyc, s.cycH = c, h
	}
	return s.cyc
}

func TestPage(t *testing.T) {
	suite.Run(t, new(PageSuite))
}

func (s *PageSuite) TestReadWritePage_RoundTrip() {
	ctx := context.Background()
	pageSize := uint32(256)
	h := &Header{PageSize: pageSize}
	data := make([]byte, pageSize)
	for i := range data {
		data[i] = byte(i)
	}
	s.Require().NoError(WritePage(ctx, s.f, h, s.cycle(h), 1, data))

	got, err := ReadPage(ctx, s.f, pageSize, 1)
	s.Require().NoError(err)
	s.Equal(data, got)
}

func (s *PageSuite) TestWritePage_GrowsFileWhenBeyondCurrentLength() {
	ctx := context.Background()
	pageSize := uint32(128)
	h := &Header{PageSize: pageSize}
	s.Require().NoError(WritePage(ctx, s.f, h, s.cycle(h), 5, make([]byte, pageSize)))

	info, err := s.f.Stat()
	s.Require().NoError(err)
	s.Equal(int64(6)*int64(pageSize), info.Size()) // pages 0..5
}

func (s *PageSuite) TestWritePage_RejectsWrongDataLength() {
	h := &Header{PageSize: 128}
	err := WritePage(context.Background(), s.f, h, s.cycle(h), 1, make([]byte, 100))
	s.ErrorIs(err, ErrInvalidPageData)
}

func (s *PageSuite) TestReadPage_ErrorsOnShortFile() {
	_, err := ReadPage(context.Background(), s.f, 128, 1) // nothing written yet
	s.ErrorIs(err, ErrShortPageRead)
}

func (s *PageSuite) TestReadWritePage_RejectsInvalidPageSize() {
	ctx := context.Background()
	tests := []uint32{0, 1, MinPageSize - 1, MaxPageSize + 1}
	for _, ps := range tests {
		h := &Header{PageSize: ps}
		c, err := NewJournalCycle(ctx, s.f, h)
		s.Require().NoError(err)
		s.ErrorIs(WritePage(ctx, s.f, h, c, 1, make([]byte, ps)), ErrInvalidPageSize)
		_, err = ReadPage(ctx, s.f, ps, 1)
		s.ErrorIs(err, ErrInvalidPageSize)
		s.Require().NoError(c.Abort(ctx, s.f, h))
	}
}

func (s *PageSuite) TestReadWriteHeader_RoundTrip() {
	ctx := context.Background()
	h, err := NewHeader(256)
	s.Require().NoError(err)
	h.PageCount = 3
	h.FreeListHead = 2
	h.CatalogHead = 1
	h.Dirty = true

	s.Require().NoError(WriteHeader(ctx, s.f, h, s.cycle(h)))

	got, err := ReadHeader(ctx, s.f)
	s.Require().NoError(err)
	s.Equal(h, got)
}

func (s *PageSuite) TestPageOffset_OverflowDetected() {
	_, err := pageOffset(math.MaxUint32, math.MaxUint32)
	s.ErrorIs(err, ErrPageOffsetOverflow)
}

func (s *PageSuite) TestPageOffset_ValidRange() {
	off, err := pageOffset(4096, 3)
	s.Require().NoError(err)
	s.Equal(int64(3*4096), off)
}

func (s *PageSuite) TestPageExistsOnDisk_OverflowDetected() {
	_, err := pageExistsOnDisk(s.f, math.MaxUint32, math.MaxUint32)
	s.ErrorIs(err, ErrPageOffsetOverflow)
}
