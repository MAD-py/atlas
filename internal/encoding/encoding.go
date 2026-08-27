// Package encoding implements Atlas's custom TLV binary codec for documents.
// It defines its own generic representation (raw id bytes + map[string]any)
// rather than the root atlas.Document, since atlas imports internal/encoding
// and not the other way around.
package encoding

import "bytes"

// IDSize is the fixed byte length of a document id (see atlas.AtlasID).
const IDSize = 12

func Marshal(id [IDSize]byte, fields map[string]any) ([]byte, error) {
	var buf bytes.Buffer
	buf.Write(id[:])
	if err := writeFields(&buf, fields); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func Unmarshal(data []byte) (id [IDSize]byte, fields map[string]any, err error) {
	if len(data) < IDSize {
		return id, nil, ErrTruncatedInput
	}
	copy(id[:], data[:IDSize])
	fields, err = readFields(data[IDSize:])
	if err != nil {
		return id, nil, err
	}
	return id, fields, nil
}
