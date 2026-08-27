package encoding

import (
	"bytes"
	"encoding/binary"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
)

type EncodeSuite struct {
	suite.Suite
}

func TestEncode(t *testing.T) {
	suite.Run(t, new(EncodeSuite))
}

func (s *EncodeSuite) encodeValueBytes(v any) []byte {
	var buf bytes.Buffer
	err := encodeValue(&buf, v)
	s.Require().NoError(err)
	return buf.Bytes()
}

// referenceVarint builds a varint via the stdlib directly, independent of
// writeUvarint, to cross-check the byte sequences asserted below.
func referenceVarint(v uint64) []byte {
	tmp := make([]byte, binary.MaxVarintLen64)
	n := binary.PutUvarint(tmp, v)
	return tmp[:n]
}

func (s *EncodeSuite) TestEncodeValue_NullAndBool() {
	s.Equal([]byte{tagNull}, s.encodeValueBytes(nil))
	s.Equal([]byte{tagTrue}, s.encodeValueBytes(true))
	s.Equal([]byte{tagFalse}, s.encodeValueBytes(false))
}

func (s *EncodeSuite) TestEncodeValue_Int() {
	tests := []struct {
		name string
		in   any
		n    int64
	}{
		{"int zero", 0, 0},
		{"int positive", 100, 100},
		{"int negative", -100, -100},
		{"int8", int8(-5), -5},
		{"int16", int16(1000), 1000},
		{"int32", int32(-100000), -100000},
		{"int64 max", int64(math.MaxInt64), math.MaxInt64},
		{"int64 min", int64(math.MinInt64), math.MinInt64},
	}

	for _, tt := range tests {
		s.Run(tt.name, func() {
			expected := append([]byte{tagInt}, referenceVarint(referenceZigzag(tt.n))...)
			s.Equal(expected, s.encodeValueBytes(tt.in))
		})
	}
}

func (s *EncodeSuite) TestEncodeValue_Float64() {
	tests := []struct {
		name string
		f    float64
	}{
		{"zero", 0},
		{"negative", -3.14159},
		{"max", math.MaxFloat64},
		{"smallest nonzero", math.SmallestNonzeroFloat64},
		{"positive infinity", math.Inf(1)},
		{"negative infinity", math.Inf(-1)},
	}

	for _, tt := range tests {
		s.Run(tt.name, func() {
			var want [8]byte
			binary.BigEndian.PutUint64(want[:], math.Float64bits(tt.f))
			expected := append([]byte{tagFloat64}, want[:]...)
			s.Equal(expected, s.encodeValueBytes(tt.f))
		})
	}
}

func (s *EncodeSuite) TestEncodeValue_String() {
	tests := []struct {
		name string
		s    string
	}{
		{"empty", ""},
		{"ascii", "hello, atlas"},
		{"multi-byte utf8", "héllo wörld 日本語 🚀"},
	}

	for _, tt := range tests {
		s.Run(tt.name, func() {
			expected := append([]byte{tagString}, referenceVarint(uint64(len(tt.s)))...)
			expected = append(expected, []byte(tt.s)...)
			s.Equal(expected, s.encodeValueBytes(tt.s))
		})
	}
}

func (s *EncodeSuite) TestEncodeValue_String_InvalidUTF8() {
	invalid := string([]byte{0xff, 0xfe, 0xfd})
	var buf bytes.Buffer
	err := encodeValue(&buf, invalid)
	s.ErrorIs(err, ErrInvalidUTF8)
}

func (s *EncodeSuite) TestEncodeValue_Array() {
	// [1, true] => tagInt+varint(zigzag(1)) followed by tagTrue.
	intBytes := append([]byte{tagInt}, referenceVarint(referenceZigzag(1))...)
	payload := append(append([]byte{}, intBytes...), tagTrue)
	expected := append([]byte{tagArray}, referenceVarint(uint64(len(payload)))...)
	expected = append(expected, payload...)

	s.Equal(expected, s.encodeValueBytes([]any{1, true}))
}

func (s *EncodeSuite) TestEncodeValue_Array_Empty() {
	s.Equal([]byte{tagArray, 0x00}, s.encodeValueBytes([]any{}))
}

func (s *EncodeSuite) TestEncodeValue_Array_PropagatesElementError() {
	var buf bytes.Buffer
	err := encodeValue(&buf, []any{struct{}{}})
	s.ErrorIs(err, ErrUnsupportedType)
}

func (s *EncodeSuite) TestEncodeValue_Object() {
	// {"x": 1} => varint(name len)+name bytes+tagInt+varint(zigzag(1))
	nameBytes := append(referenceVarint(uint64(len("x"))), []byte("x")...)
	intBytes := append([]byte{tagInt}, referenceVarint(referenceZigzag(1))...)
	payload := append(nameBytes, intBytes...)
	expected := append([]byte{tagObject}, referenceVarint(uint64(len(payload)))...)
	expected = append(expected, payload...)

	s.Equal(expected, s.encodeValueBytes(map[string]any{"x": 1}))
}

func (s *EncodeSuite) TestEncodeValue_Object_Empty() {
	s.Equal([]byte{tagObject, 0x00}, s.encodeValueBytes(map[string]any{}))
}

func (s *EncodeSuite) TestEncodeValue_Object_PropagatesFieldError() {
	var buf bytes.Buffer
	err := encodeValue(&buf, map[string]any{"x": struct{}{}})
	s.ErrorIs(err, ErrUnsupportedType)
}

func (s *EncodeSuite) TestEncodeValue_Object_InvalidUTF8FieldName() {
	invalid := string([]byte{0xff, 0xfe, 0xfd})
	var buf bytes.Buffer
	err := encodeValue(&buf, map[string]any{invalid: 1})
	s.ErrorIs(err, ErrInvalidUTF8)
}

func (s *EncodeSuite) TestEncodeValue_Date() {
	tests := []struct {
		name string
		date time.Time
		days int32
	}{
		{"epoch", time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC), 0},
		{"one day after epoch", time.Date(1970, 1, 2, 0, 0, 0, 0, time.UTC), 1},
		{"one day before epoch", time.Date(1969, 12, 31, 0, 0, 0, 0, time.UTC), -1},
		{"time component is discarded", time.Date(1970, 1, 2, 23, 59, 59, 999, time.UTC), 1},
	}

	for _, tt := range tests {
		s.Run(tt.name, func() {
			var want [4]byte
			binary.BigEndian.PutUint32(want[:], uint32(tt.days))
			expected := append([]byte{tagDate}, want[:]...)
			s.Equal(expected, s.encodeValueBytes(Date(tt.date)))
		})
	}
}

func (s *EncodeSuite) TestEncodeValue_Date_OutOfInt32Range() {
	farFuture := time.Date(6000000, 1, 1, 0, 0, 0, 0, time.UTC)
	var buf bytes.Buffer
	err := encodeValue(&buf, Date(farFuture))
	s.ErrorIs(err, ErrUnsupportedType)
}

func (s *EncodeSuite) TestEncodeValue_Timestamp() {
	tests := []struct {
		name string
		ts   time.Time
	}{
		{"epoch", time.Unix(0, 0).UTC()},
		{"with nanoseconds", time.Date(2024, 3, 15, 10, 30, 45, 123456789, time.UTC)},
		{"pre-epoch", time.Date(1965, 1, 1, 0, 0, 0, 0, time.UTC)},
		{"non-utc input normalized to utc", time.Date(2024, 3, 15, 10, 0, 0, 0, time.FixedZone("X", 3600))},
	}

	for _, tt := range tests {
		s.Run(tt.name, func() {
			var want [8]byte
			binary.BigEndian.PutUint64(want[:], uint64(tt.ts.UTC().UnixNano()))
			expected := append([]byte{tagTimestamp}, want[:]...)
			s.Equal(expected, s.encodeValueBytes(tt.ts))
		})
	}
}

func (s *EncodeSuite) TestEncodeValue_UnsupportedType() {
	var buf bytes.Buffer
	err := encodeValue(&buf, struct{ A int }{A: 1})
	s.ErrorIs(err, ErrUnsupportedType)
}

func (s *EncodeSuite) TestWriteFields_InvalidUTF8FieldName() {
	invalid := string([]byte{0xff, 0xfe, 0xfd})
	var buf bytes.Buffer
	err := writeFields(&buf, map[string]any{invalid: 1})
	s.ErrorIs(err, ErrInvalidUTF8)
}
