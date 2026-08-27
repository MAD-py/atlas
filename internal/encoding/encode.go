package encoding

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
	"time"
	"unicode/utf8"
)

func writeFields(w *bytes.Buffer, fields map[string]any) error {
	for name, value := range fields {
		if !utf8.ValidString(name) {
			return ErrInvalidUTF8
		}
		writeUvarint(w, uint64(len(name)))
		w.WriteString(name)
		if err := encodeValue(w, value); err != nil {
			return err
		}
	}
	return nil
}

func encodeValue(w *bytes.Buffer, v any) error {
	switch val := v.(type) {
	case nil:
		w.WriteByte(tagNull)
		return nil
	case bool:
		if val {
			w.WriteByte(tagTrue)
		} else {
			w.WriteByte(tagFalse)
		}
		return nil
	case int:
		return writeInt(w, int64(val))
	case int8:
		return writeInt(w, int64(val))
	case int16:
		return writeInt(w, int64(val))
	case int32:
		return writeInt(w, int64(val))
	case int64:
		return writeInt(w, val)
	case float64:
		return writeFloat64(w, val)
	case string:
		return writeString(w, val)
	case []any:
		return writeArray(w, val)
	case map[string]any:
		return writeObject(w, val)
	case Date:
		return writeDate(w, val)
	case time.Time:
		return writeTimestamp(w, val)
	default:
		return fmt.Errorf("%w: %T", ErrUnsupportedType, v)
	}
}

func writeInt(w *bytes.Buffer, n int64) error {
	w.WriteByte(tagInt)
	writeUvarint(w, zigzagEncode(n))
	return nil
}

func writeFloat64(w *bytes.Buffer, f float64) error {
	w.WriteByte(tagFloat64)
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], math.Float64bits(f))
	w.Write(buf[:])
	return nil
}

func writeString(w *bytes.Buffer, s string) error {
	if !utf8.ValidString(s) {
		return ErrInvalidUTF8
	}
	w.WriteByte(tagString)
	writeUvarint(w, uint64(len(s)))
	w.WriteString(s)
	return nil
}

func writeArray(w *bytes.Buffer, arr []any) error {
	var payload bytes.Buffer
	for _, elem := range arr {
		if err := encodeValue(&payload, elem); err != nil {
			return err
		}
	}
	w.WriteByte(tagArray)
	writeUvarint(w, uint64(payload.Len()))
	w.Write(payload.Bytes())
	return nil
}

func writeObject(w *bytes.Buffer, obj map[string]any) error {
	var payload bytes.Buffer
	if err := writeFields(&payload, obj); err != nil {
		return err
	}
	w.WriteByte(tagObject)
	writeUvarint(w, uint64(payload.Len()))
	w.Write(payload.Bytes())
	return nil
}

func writeDate(w *bytes.Buffer, d Date) error {
	t := time.Time(d)
	midnight := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	days := midnight.Unix() / secondsPerDay
	if days < math.MinInt32 || days > math.MaxInt32 {
		return fmt.Errorf("%w: date out of int32 day range", ErrUnsupportedType)
	}
	w.WriteByte(tagDate)
	var buf [4]byte
	binary.BigEndian.PutUint32(buf[:], uint32(days))
	w.Write(buf[:])
	return nil
}

func writeTimestamp(w *bytes.Buffer, t time.Time) error {
	w.WriteByte(tagTimestamp)
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], uint64(t.UTC().UnixNano()))
	w.Write(buf[:])
	return nil
}
