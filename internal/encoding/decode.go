package encoding

import (
	"encoding/binary"
	"fmt"
	"math"
	"time"
	"unicode/utf8"
)

type decoder struct {
	buf []byte
	pos int
}

func (d *decoder) readByte() (byte, error) {
	if d.pos >= len(d.buf) {
		return 0, ErrTruncatedInput
	}
	b := d.buf[d.pos]
	d.pos++
	return b, nil
}

func (d *decoder) readBytes(n int) ([]byte, error) {
	// Compare against remaining space rather than d.pos+n > len(d.buf): a
	// corrupted/malicious length near math.MaxInt would overflow that sum
	// and wrap negative, bypassing the check and panicking on the slice
	// below instead of returning a decode error.
	if n < 0 || n > len(d.buf)-d.pos {
		return nil, ErrTruncatedInput
	}
	b := d.buf[d.pos : d.pos+n]
	d.pos += n
	return b, nil
}

func (d *decoder) readUvarint() (uint64, error) {
	v, n := binary.Uvarint(d.buf[d.pos:])
	if n == 0 {
		return 0, ErrTruncatedInput
	}
	if n < 0 {
		return 0, ErrCorruptedData
	}
	d.pos += n
	return v, nil
}

func (d *decoder) readLengthPrefixed() ([]byte, error) {
	n, err := d.readUvarint()
	if err != nil {
		return nil, err
	}
	if n > uint64(math.MaxInt) {
		return nil, ErrCorruptedData
	}
	return d.readBytes(int(n))
}

func (d *decoder) readFieldName() (string, error) {
	b, err := d.readLengthPrefixed()
	if err != nil {
		return "", err
	}
	if !utf8.Valid(b) {
		return "", ErrInvalidUTF8
	}
	return string(b), nil
}

func (d *decoder) readValue() (any, error) {
	tag, err := d.readByte()
	if err != nil {
		return nil, err
	}
	switch tag {
	case tagNull:
		return nil, nil
	case tagFalse:
		return false, nil
	case tagTrue:
		return true, nil
	case tagInt:
		u, err := d.readUvarint()
		if err != nil {
			return nil, err
		}
		return zigzagDecode(u), nil
	case tagFloat64:
		b, err := d.readBytes(8)
		if err != nil {
			return nil, err
		}
		return math.Float64frombits(binary.BigEndian.Uint64(b)), nil
	case tagString:
		b, err := d.readLengthPrefixed()
		if err != nil {
			return nil, err
		}
		if !utf8.Valid(b) {
			return nil, ErrInvalidUTF8
		}
		return string(b), nil
	case tagArray:
		return d.readArray()
	case tagObject:
		return d.readObject()
	case tagDate:
		b, err := d.readBytes(4)
		if err != nil {
			return nil, err
		}
		days := int32(binary.BigEndian.Uint32(b))
		return Date(time.Unix(int64(days)*secondsPerDay, 0).UTC()), nil
	case tagTimestamp:
		b, err := d.readBytes(8)
		if err != nil {
			return nil, err
		}
		ns := int64(binary.BigEndian.Uint64(b))
		return time.Unix(0, ns).UTC(), nil
	default:
		return nil, fmt.Errorf("%w: 0x%02x", ErrUnknownTag, tag)
	}
}

func (d *decoder) readArray() ([]any, error) {
	payload, err := d.readLengthPrefixed()
	if err != nil {
		return nil, err
	}
	sub := &decoder{buf: payload}
	arr := []any{}
	for sub.pos < len(sub.buf) {
		v, err := sub.readValue()
		if err != nil {
			return nil, err
		}
		arr = append(arr, v)
	}
	return arr, nil
}

func (d *decoder) readObject() (map[string]any, error) {
	payload, err := d.readLengthPrefixed()
	if err != nil {
		return nil, err
	}
	return readFields(payload)
}

func readFields(buf []byte) (map[string]any, error) {
	d := &decoder{buf: buf}
	fields := make(map[string]any)
	for d.pos < len(d.buf) {
		name, err := d.readFieldName()
		if err != nil {
			return nil, err
		}
		v, err := d.readValue()
		if err != nil {
			return nil, err
		}
		fields[name] = v
	}
	return fields, nil
}
