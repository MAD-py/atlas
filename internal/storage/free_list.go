package storage

import (
	"encoding/binary"
	"math"
	"os"
)

const freePagePrefixSize = 5 // page_type (1 byte) + next_free_page (uint32)

func encodeFreePage(pageSize, nextFree uint32) []byte {
	buf := make([]byte, pageSize)
	buf[0] = byte(PageTypeFree)
	binary.BigEndian.PutUint32(buf[1:5], nextFree)
	return buf
}

func decodeFreePageNext(buf []byte) (uint32, error) {
	if len(buf) < freePagePrefixSize {
		return 0, ErrShortPageRead
	}
	if PageType(buf[0]) != PageTypeFree {
		return 0, ErrNotAFreePage
	}
	return binary.BigEndian.Uint32(buf[1:5]), nil
}

// Allocate returns the free-list head if non-empty, or a newly grown page
// otherwise. h is only mutated after the header write to disk succeeds, so
// a failed Allocate never leaves h ahead of what's on disk.
func Allocate(f *os.File, h *Header) (uint32, error) {
	if h.FreeListHead != 0 {
		pageNum := h.FreeListHead
		buf, err := ReadPage(f, h.PageSize, pageNum)
		if err != nil {
			return 0, err
		}
		next, err := decodeFreePageNext(buf)
		if err != nil {
			return 0, err
		}
		updated := *h
		updated.FreeListHead = next
		if err := WriteHeader(f, &updated); err != nil {
			return 0, err
		}
		*h = updated
		return pageNum, nil
	}

	if h.PageCount == math.MaxUint32 {
		return 0, ErrPageCountOverflow
	}
	pageNum := h.PageCount + 1
	blank := make([]byte, h.PageSize)
	if err := WritePage(f, h.PageSize, pageNum, blank); err != nil {
		return 0, err
	}
	updated := *h
	updated.PageCount = pageNum
	if err := WriteHeader(f, &updated); err != nil {
		return 0, err
	}
	*h = updated
	return pageNum, nil
}

// Free pushes pageNum onto the free-list head.
func Free(f *os.File, h *Header, pageNum uint32) error {
	if pageNum == 0 || pageNum > h.PageCount {
		return ErrInvalidPageNumber
	}
	buf := encodeFreePage(h.PageSize, h.FreeListHead)
	if err := WritePage(f, h.PageSize, pageNum, buf); err != nil {
		return err
	}
	updated := *h
	updated.FreeListHead = pageNum
	if err := WriteHeader(f, &updated); err != nil {
		return err
	}
	*h = updated
	return nil
}
