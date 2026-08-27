package encoding

import (
	"testing"

	"github.com/stretchr/testify/suite"
)

type EncodingSuite struct {
	suite.Suite
}

func TestEncoding(t *testing.T) {
	suite.Run(t, new(EncodingSuite))
}

func sampleID() [IDSize]byte {
	return [IDSize]byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c}
}

// Type coverage for individual values lives in encode_test.go/decode_test.go;
// this only exercises Marshal/Unmarshal's own job: prepending/extracting the
// id and delegating the rest to writeFields/readFields.
func (s *EncodingSuite) TestMarshalUnmarshal_RoundTrip() {
	tests := []struct {
		name     string
		input    map[string]any
		expected map[string]any
	}{
		{
			name:     "empty document",
			input:    map[string]any{},
			expected: map[string]any{},
		},
		{
			name: "combined document with nested types",
			input: map[string]any{
				"name":   "Atlas",
				"age":    5,
				"active": true,
				"score":  9.5,
				"tags":   []any{"db", "go"},
				"meta":   map[string]any{"v": 1},
				"note":   nil,
			},
			expected: map[string]any{
				"name":   "Atlas",
				"age":    int64(5),
				"active": true,
				"score":  9.5,
				"tags":   []any{"db", "go"},
				"meta":   map[string]any{"v": int64(1)},
				"note":   nil,
			},
		},
	}

	for _, tt := range tests {
		s.Run(tt.name, func() {
			id := sampleID()
			data, err := Marshal(id, tt.input)
			s.Require().NoError(err)

			gotID, gotFields, err := Unmarshal(data)
			s.Require().NoError(err)
			s.Equal(id, gotID)
			s.Equal(tt.expected, gotFields)
		})
	}
}

func (s *EncodingSuite) TestMarshal_PrependsIDVerbatim() {
	id := sampleID()
	data, err := Marshal(id, map[string]any{"a": 1})
	s.Require().NoError(err)
	s.Equal(id[:], data[:IDSize])
}

func (s *EncodingSuite) TestMarshal_PropagatesFieldEncodingError() {
	id := sampleID()
	_, err := Marshal(id, map[string]any{"x": struct{}{}})
	s.ErrorIs(err, ErrUnsupportedType)
}

func (s *EncodingSuite) TestUnmarshal_ExtractsIDVerbatim() {
	id := sampleID()
	data, err := Marshal(id, map[string]any{})
	s.Require().NoError(err)

	gotID, _, err := Unmarshal(data)
	s.Require().NoError(err)
	s.Equal(id, gotID)
}

func (s *EncodingSuite) TestUnmarshal_DataShorterThanIDSize() {
	tests := []int{0, 1, IDSize - 1}

	for _, n := range tests {
		_, _, err := Unmarshal(make([]byte, n))
		s.ErrorIs(err, ErrTruncatedInput)
	}
}

func (s *EncodingSuite) TestUnmarshal_PropagatesFieldDecodingError() {
	id := sampleID()
	data := append(id[:], 0x01, 'a', 0xfe) // unknown tag inside the fields region

	_, _, err := Unmarshal(data)
	s.ErrorIs(err, ErrUnknownTag)
}
