package storage

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
)

// journalHeaderSize is the fixed on-disk size of the journal file's own header:
//
//	Offset  Size  Field
//	0       8     magic bytes ("ATLASJRL"), distinct from the .db's own
//	8       4     page-record count (uint32)
//	12      4     header checksum, CRC32 over bytes [0:12)
const journalHeaderSize = 16

var journalMagic = [8]byte{'A', 'T', 'L', 'A', 'S', 'J', 'R', 'L'}

// journalRecordPageNumSize/journalRecordChecksumSize are the fixed-size
// fields flanking a record's page_size-byte original_content.
const (
	journalRecordPageNumSize  = 4
	journalRecordChecksumSize = 4
)

// journalRecordSize is page_number(4) + original_content(pageSize) +
// checksum(4) — every record on a given journal is this same fixed size,
// since a journal only ever exists for one .db whose page size is fixed.
func journalRecordSize(pageSize uint32) int64 {
	return journalRecordPageNumSize + int64(pageSize) + journalRecordChecksumSize
}

func encodeJournalHeader(recordCount uint32) []byte {
	buf := make([]byte, journalHeaderSize)
	copy(buf[0:8], journalMagic[:])
	binary.BigEndian.PutUint32(buf[8:12], recordCount)
	checksum := crc32.ChecksumIEEE(buf[0:12])
	binary.BigEndian.PutUint32(buf[12:16], checksum)
	return buf
}

func decodeJournalHeader(buf []byte) (recordCount uint32, err error) {
	if len(buf) < journalHeaderSize {
		return 0, ErrTruncatedJournalHeader
	}
	if !bytes.Equal(buf[0:8], journalMagic[:]) {
		return 0, ErrNotAJournalFile
	}
	wantChecksum := binary.BigEndian.Uint32(buf[12:16])
	gotChecksum := crc32.ChecksumIEEE(buf[0:12])
	if wantChecksum != gotChecksum {
		return 0, ErrCorruptedJournalHeader
	}
	return binary.BigEndian.Uint32(buf[8:12]), nil
}

// encodeJournalRecord's checksum covers page_number too, not just
// original_content — a torn write mid-append could corrupt either field, and
// this way one checksum catches both instead of only the content half.
func encodeJournalRecord(pageNum uint32, content []byte) []byte {
	buf := make([]byte, journalRecordPageNumSize+len(content)+journalRecordChecksumSize)
	binary.BigEndian.PutUint32(buf[0:4], pageNum)
	copy(buf[4:4+len(content)], content)
	checksum := crc32.ChecksumIEEE(buf[0 : 4+len(content)])
	binary.BigEndian.PutUint32(buf[4+len(content):], checksum)
	return buf
}

func decodeJournalRecord(buf []byte, pageSize uint32) (pageNum uint32, content []byte, err error) {
	want := journalRecordSize(pageSize)
	if int64(len(buf)) != want {
		return 0, nil, ErrShortJournalRecord
	}
	contentEnd := journalRecordPageNumSize + int(pageSize)
	wantChecksum := binary.BigEndian.Uint32(buf[contentEnd:])
	gotChecksum := crc32.ChecksumIEEE(buf[0:contentEnd])
	if wantChecksum != gotChecksum {
		return 0, nil, ErrCorruptedJournalRecord
	}
	pageNum = binary.BigEndian.Uint32(buf[0:4])
	content = append([]byte(nil), buf[journalRecordPageNumSize:contentEnd]...)
	return pageNum, content, nil
}
