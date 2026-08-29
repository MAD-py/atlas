package storage

import (
	"testing"

	"github.com/stretchr/testify/suite"
)

type JournalFormatSuite struct {
	suite.Suite
}

func TestJournalFormat(t *testing.T) {
	suite.Run(t, new(JournalFormatSuite))
}

func (s *JournalFormatSuite) TestJournalHeader_EncodeDecode_RoundTrip() {
	tests := []uint32{0, 1, 7, 1 << 20}
	for _, count := range tests {
		buf := encodeJournalHeader(count)
		s.Len(buf, journalHeaderSize)

		got, err := decodeJournalHeader(buf)
		s.Require().NoError(err)
		s.Equal(count, got)
	}
}

func (s *JournalFormatSuite) TestDecodeJournalHeader_RejectsShortBuffer() {
	tests := []int{0, 1, journalHeaderSize - 1}
	for _, n := range tests {
		_, err := decodeJournalHeader(make([]byte, n))
		s.ErrorIs(err, ErrTruncatedJournalHeader)
	}
}

func (s *JournalFormatSuite) TestDecodeJournalHeader_RejectsBadMagic() {
	buf := encodeJournalHeader(3)
	buf[0] = 'X'
	_, err := decodeJournalHeader(buf)
	s.ErrorIs(err, ErrNotAJournalFile)
}

func (s *JournalFormatSuite) TestDecodeJournalHeader_DetectsChecksumCorruption() {
	buf := encodeJournalHeader(3)
	buf[8] ^= 0xFF // flip a byte inside the checksummed count field
	_, err := decodeJournalHeader(buf)
	s.ErrorIs(err, ErrCorruptedJournalHeader)
}

func (s *JournalFormatSuite) TestJournalRecord_EncodeDecode_RoundTrip() {
	tests := []struct {
		name    string
		pageNum uint32
		content []byte
	}{
		{"small page", 1, []byte("hello world")},
		{"zero page number", 0, make([]byte, 64)},
		{"all-zero content", 5, make([]byte, 32)},
	}
	for _, tt := range tests {
		s.Run(tt.name, func() {
			buf := encodeJournalRecord(tt.pageNum, tt.content)
			s.Len(buf, int(journalRecordSize(uint32(len(tt.content)))))

			gotPage, gotContent, err := decodeJournalRecord(buf, uint32(len(tt.content)))
			s.Require().NoError(err)
			s.Equal(tt.pageNum, gotPage)
			s.Equal(tt.content, gotContent)
		})
	}
}

func (s *JournalFormatSuite) TestDecodeJournalRecord_RejectsWrongLength() {
	buf := encodeJournalRecord(1, make([]byte, 32))
	_, _, err := decodeJournalRecord(buf[:len(buf)-1], 32)
	s.ErrorIs(err, ErrShortJournalRecord)
}

func (s *JournalFormatSuite) TestDecodeJournalRecord_DetectsCorruptionInContentOrPageNumber() {
	tests := []struct {
		name string
		idx  int
	}{
		{"corrupt page_number byte", 0},
		{"corrupt content byte", journalRecordPageNumSize + 2},
	}
	content := []byte("original content")
	for _, tt := range tests {
		s.Run(tt.name, func() {
			buf := encodeJournalRecord(7, content)
			buf[tt.idx] ^= 0xFF
			_, _, err := decodeJournalRecord(buf, uint32(len(content)))
			s.ErrorIs(err, ErrCorruptedJournalRecord)
		})
	}
}
