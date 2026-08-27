package encoding

import (
	"bytes"
	"math"
	"testing"

	"github.com/stretchr/testify/suite"
)

type VarintSuite struct {
	suite.Suite
}

func TestVarint(t *testing.T) {
	suite.Run(t, new(VarintSuite))
}

// referenceZigzag mirrors the standard zigzag definition via plain
// arithmetic instead of the bit-trick in zigzagEncode, so it checks
// zigzagEncode independently rather than against its own formula.
func referenceZigzag(n int64) uint64 {
	if n == math.MinInt64 {
		return math.MaxUint64
	}
	if n >= 0 {
		return uint64(n) * 2
	}
	return uint64(-n)*2 - 1
}

func (s *VarintSuite) TestZigzagEncode_MatchesKnownMapping() {
	tests := []struct {
		n        int64
		expected uint64
	}{
		{0, 0},
		{-1, 1},
		{1, 2},
		{-2, 3},
		{2, 4},
		{-3, 5},
		{math.MaxInt64, math.MaxUint64 - 1},
		{math.MinInt64, math.MaxUint64},
	}

	for _, tt := range tests {
		s.Equal(tt.expected, zigzagEncode(tt.n), "zigzagEncode(%d)", tt.n)
		s.Equal(tt.expected, referenceZigzag(tt.n), "referenceZigzag(%d)", tt.n)
	}
}

func (s *VarintSuite) TestZigzagRoundTrip() {
	values := []int64{
		0, 1, -1, 2, -2, 42, -42,
		math.MaxInt32, math.MinInt32,
		math.MaxInt64, math.MinInt64,
	}
	for _, v := range values {
		s.Equal(v, zigzagDecode(zigzagEncode(v)), "value %d", v)
	}
}

func (s *VarintSuite) TestWriteUvarint_ByteLengthBoundaries() {
	tests := []struct {
		name        string
		value       uint64
		expectedLen int
	}{
		{"zero fits in one byte", 0, 1},
		{"127 is the largest one-byte value", 127, 1},
		{"128 needs two bytes", 128, 2},
		{"16383 is the largest two-byte value", 16383, 2},
		{"16384 needs three bytes", 16384, 3},
		{"max uint32 needs five bytes", math.MaxUint32, 5},
		{"max uint64 needs ten bytes", math.MaxUint64, 10},
	}

	for _, tt := range tests {
		s.Run(tt.name, func() {
			var buf bytes.Buffer
			writeUvarint(&buf, tt.value)
			s.Len(buf.Bytes(), tt.expectedLen)
		})
	}
}

func (s *VarintSuite) TestWriteUvarint_ReadUvarintRoundTrip() {
	values := []uint64{0, 1, 127, 128, 16383, 16384, math.MaxUint32, math.MaxUint64}

	for _, v := range values {
		var buf bytes.Buffer
		writeUvarint(&buf, v)

		d := &decoder{buf: buf.Bytes()}
		got, err := d.readUvarint()
		s.Require().NoError(err)
		s.Equal(v, got)
		s.Equal(buf.Len(), d.pos, "readUvarint should consume exactly what writeUvarint wrote")
	}
}
