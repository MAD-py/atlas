package encoding

import (
	"bytes"
	"encoding/binary"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
)

type DecodeSuite struct {
	suite.Suite
}

func TestDecode(t *testing.T) {
	suite.Run(t, new(DecodeSuite))
}

func (s *DecodeSuite) readValue(buf []byte) (any, error) {
	d := &decoder{buf: buf}
	v, err := d.readValue()
	if err == nil {
		s.Equal(len(buf), d.pos, "readValue should consume the whole input")
	}
	return v, err
}

func (s *DecodeSuite) TestReadValue_NullAndBool() {
	v, err := s.readValue([]byte{tagNull})
	s.Require().NoError(err)
	s.Nil(v)

	v, err = s.readValue([]byte{tagTrue})
	s.Require().NoError(err)
	s.Equal(true, v)

	v, err = s.readValue([]byte{tagFalse})
	s.Require().NoError(err)
	s.Equal(false, v)
}

func (s *DecodeSuite) TestReadValue_Int() {
	tests := []struct {
		name string
		n    int64
	}{
		{"zero", 0},
		{"positive", 100},
		{"negative", -100},
		{"max int64", math.MaxInt64},
		{"min int64", math.MinInt64},
	}

	for _, tt := range tests {
		s.Run(tt.name, func() {
			buf := append([]byte{tagInt}, referenceVarint(referenceZigzag(tt.n))...)
			v, err := s.readValue(buf)
			s.Require().NoError(err)
			s.Equal(tt.n, v)
		})
	}
}

func (s *DecodeSuite) TestReadValue_Float64() {
	tests := []float64{0, -3.14159, math.MaxFloat64, math.SmallestNonzeroFloat64, math.Inf(1), math.Inf(-1)}

	for _, f := range tests {
		var want [8]byte
		binary.BigEndian.PutUint64(want[:], math.Float64bits(f))
		buf := append([]byte{tagFloat64}, want[:]...)

		v, err := s.readValue(buf)
		s.Require().NoError(err)
		s.Equal(f, v)
	}
}

func (s *DecodeSuite) TestReadValue_String() {
	tests := []string{"", "hello, atlas", "héllo wörld 日本語 🚀"}

	for _, str := range tests {
		buf := append([]byte{tagString}, referenceVarint(uint64(len(str)))...)
		buf = append(buf, []byte(str)...)

		v, err := s.readValue(buf)
		s.Require().NoError(err)
		s.Equal(str, v)
	}
}

func (s *DecodeSuite) TestReadValue_String_InvalidUTF8() {
	buf := append([]byte{tagString}, referenceVarint(2)...)
	buf = append(buf, 0xff, 0xfe)

	_, err := s.readValue(buf)
	s.ErrorIs(err, ErrInvalidUTF8)
}

func (s *DecodeSuite) TestReadValue_Array() {
	// [1, true]
	intBytes := append([]byte{tagInt}, referenceVarint(referenceZigzag(1))...)
	payload := append(append([]byte{}, intBytes...), tagTrue)
	buf := append([]byte{tagArray}, referenceVarint(uint64(len(payload)))...)
	buf = append(buf, payload...)

	v, err := s.readValue(buf)
	s.Require().NoError(err)
	s.Equal([]any{int64(1), true}, v)
}

func (s *DecodeSuite) TestReadValue_Array_Empty() {
	v, err := s.readValue([]byte{tagArray, 0x00})
	s.Require().NoError(err)
	s.Equal([]any{}, v)
}

func (s *DecodeSuite) TestReadValue_Object() {
	nameBytes := append(referenceVarint(uint64(len("x"))), []byte("x")...)
	intBytes := append([]byte{tagInt}, referenceVarint(referenceZigzag(1))...)
	payload := append(nameBytes, intBytes...)
	buf := append([]byte{tagObject}, referenceVarint(uint64(len(payload)))...)
	buf = append(buf, payload...)

	v, err := s.readValue(buf)
	s.Require().NoError(err)
	s.Equal(map[string]any{"x": int64(1)}, v)
}

func (s *DecodeSuite) TestReadValue_Object_Empty() {
	v, err := s.readValue([]byte{tagObject, 0x00})
	s.Require().NoError(err)
	s.Equal(map[string]any{}, v)
}

func (s *DecodeSuite) TestReadValue_Date() {
	tests := []struct {
		name    string
		days    int32
		y, m, d int
	}{
		{"epoch", 0, 1970, 1, 1},
		{"one day after epoch", 1, 1970, 1, 2},
		{"one day before epoch", -1, 1969, 12, 31},
	}

	for _, tt := range tests {
		s.Run(tt.name, func() {
			var want [4]byte
			binary.BigEndian.PutUint32(want[:], uint32(tt.days))
			buf := append([]byte{tagDate}, want[:]...)

			v, err := s.readValue(buf)
			s.Require().NoError(err)

			got, ok := v.(Date)
			s.Require().True(ok)
			gotTime := time.Time(got)
			s.Equal(tt.y, gotTime.Year())
			s.Equal(time.Month(tt.m), gotTime.Month())
			s.Equal(tt.d, gotTime.Day())
			s.Equal(time.UTC, gotTime.Location())
		})
	}
}

func (s *DecodeSuite) TestReadValue_Timestamp() {
	ns := time.Date(2024, 3, 15, 10, 30, 45, 123456789, time.UTC).UnixNano()
	var want [8]byte
	binary.BigEndian.PutUint64(want[:], uint64(ns))
	buf := append([]byte{tagTimestamp}, want[:]...)

	v, err := s.readValue(buf)
	s.Require().NoError(err)

	got, ok := v.(time.Time)
	s.Require().True(ok)
	s.Equal(ns, got.UnixNano())
	s.Equal(time.UTC, got.Location())
}

func (s *DecodeSuite) TestReadValue_UnknownTag() {
	_, err := s.readValue([]byte{0xfe})
	s.ErrorIs(err, ErrUnknownTag)
}

func (s *DecodeSuite) TestReadValue_Truncated() {
	tests := []struct {
		name string
		buf  []byte
	}{
		{"empty input", []byte{}},
		{"int tag with no varint payload", []byte{tagInt}},
		{"float64 tag with too few bytes", []byte{tagFloat64, 0x00, 0x00, 0x00}},
		{"string tag declares length longer than remaining bytes", []byte{tagString, 0x05}},
		{"array tag declares length longer than remaining bytes", []byte{tagArray, 0x7f}},
		{"object tag declares length longer than remaining bytes", []byte{tagObject, 0x7f}},
		{"date tag with too few bytes", []byte{tagDate, 0x00, 0x00}},
		{"timestamp tag with too few bytes", []byte{tagTimestamp, 0x00, 0x00, 0x00, 0x00}},
	}

	for _, tt := range tests {
		s.Run(tt.name, func() {
			d := &decoder{buf: tt.buf}
			_, err := d.readValue()
			s.ErrorIs(err, ErrTruncatedInput)
		})
	}
}

// A declared length near math.MaxInt must not overflow d.pos+n into a
// negative bound and panic on the slice — it must be reported as truncated
// input, same as any other length that doesn't fit the remaining buffer.
func (s *DecodeSuite) TestReadValue_DeclaredLengthNearMaxIntDoesNotPanic() {
	tests := []struct {
		name string
		tag  byte
	}{
		{"string", tagString},
		{"array", tagArray},
		{"object", tagObject},
	}

	for _, tt := range tests {
		s.Run(tt.name, func() {
			buf := append([]byte{tt.tag}, referenceVarint(uint64(math.MaxInt))...)
			d := &decoder{buf: buf}

			s.NotPanics(func() {
				_, err := d.readValue()
				s.ErrorIs(err, ErrTruncatedInput)
			})
		})
	}
}

func (s *DecodeSuite) TestReadValue_CorruptedVarintOverflow() {
	d := &decoder{buf: bytes.Repeat([]byte{0xff}, 11)}
	_, err := d.readUvarint()
	s.ErrorIs(err, ErrCorruptedData)
}

// The array/object sub-decoder must be bounded to the declared payload
// length, not able to read past it into whatever bytes follow in the
// parent buffer — otherwise a corrupted length would silently decode
// unrelated sibling bytes instead of erroring.
func (s *DecodeSuite) TestReadArray_PayloadIsBoundedToDeclaredLength() {
	// tagArray, payload length = 1 (only the int tag byte), then a
	// trailing byte that WOULD decode as a valid single-byte varint (5)
	// if the sub-decoder incorrectly leaked into the parent buffer.
	buf := []byte{tagArray, 0x01, tagInt, 0x05, 0xbb}

	d := &decoder{buf: buf}
	_, err := d.readValue()
	s.ErrorIs(err, ErrTruncatedInput)
}

func (s *DecodeSuite) TestReadFields_TruncatedInput() {
	tests := []struct {
		name string
		buf  []byte
	}{
		{"field name length declared but no name bytes follow", []byte{0x01}},
		{"field name ok but no value tag follows", []byte{0x01, 'a'}},
	}

	for _, tt := range tests {
		s.Run(tt.name, func() {
			_, err := readFields(tt.buf)
			s.ErrorIs(err, ErrTruncatedInput)
		})
	}
}

func (s *DecodeSuite) TestReadFields_InvalidUTF8FieldName() {
	buf := []byte{0x03, 0xff, 0xfe, 0xfd, tagNull}
	_, err := readFields(buf)
	s.ErrorIs(err, ErrInvalidUTF8)
}

func (s *DecodeSuite) TestReadFields_Empty() {
	fields, err := readFields([]byte{})
	s.Require().NoError(err)
	s.Equal(map[string]any{}, fields)
}
