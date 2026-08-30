# internal/storage — on-disk layout

This document describes the exact byte layout of the `.db` file: the header, and every page kind that can follow it.

All multi-byte fields are **big-endian**. Every page is exactly one fixed `page_size` (4096 bytes by default), read and written as a whole — nothing here does partial-page I/O.

## 1. The file as a sequence of pages

Page 0 is always the file header. Every page after it holds one of four kinds of content, distinguished by a leading `page_type` byte shared across all of them:

| `page_type` | Kind |
|---|---|
| `0x00` | invalid — the zero value, never a real page's on-disk type |
| `0x01` | free-list page |
| `0x02` | catalog page |
| `0x03` | data page |

Pages link to each other by page number (a `uint32`, `0` meaning "none") instead of a memory address — the same singly-linked-list idea, just with a page number standing in for a pointer:

```
[ page 0: header ]
        │
        ├─ CatalogHead ──▶ [ catalog page ] ──next──▶ [ catalog page ] ──▶ 0
        │
        └─ FreeListHead ─▶ [ free page ] ──next_free──▶ [ free page ] ──▶ 0
```

Each collection has its own separate chain of data pages, reached through its catalog slot's `Head`/`Tail` pointers (§4).

## 2. File header (page 0)

64 bytes, packed at the front of page 0; the rest of that page is zero-padded.

| Offset | Size | Field |
|---|---|---|
| 0 | 8 | magic bytes (`"ATLASDB\0"`) |
| 8 | 4 | format version (`uint32`) |
| 12 | 4 | page size in bytes (`uint32`, fixed at file creation) |
| 16 | 4 | total page count, excluding the header page itself (`uint32`) |
| 20 | 4 | free-list head page number, `0` = empty (`uint32`) |
| 24 | 4 | catalog head page number, `0` = none yet (`uint32`) |
| 28 | 1 | dirty flag (`0` = clean, `1` = a write is in progress) |
| 29 | 3 | reserved (zero) |
| 32 | 4 | header checksum, CRC32 over bytes `[0:32)` |
| 36 | 28 | reserved (zero) |

```
┌───────┬─────┬────────┬─────────┬───────┬─────────┬────┬─────┬───────┬──────────┐
│ magic │ ver │ pgsize │ pgcount │ flist │ catalog │ D  │ rsv │ CRC32 │ reserved │
└───────┴─────┴────────┴─────────┴───────┴─────────┴────┴─────┴───────┴──────────┘
 0       8     12       16        20      24        28   29    32      36         64
```

## 3. Free-list page

A free page is a 5-byte prefix; the rest of the page is unused.

| Offset | Size | Field |
|---|---|---|
| 0 | 1 | `page_type` (`0x01`) |
| 1 | 4 | `next_free_page`, `0` = end of list (`uint32`) |

```
┌──────┬────────────────┬────────────────┐
│ type │ next_free_page │ ... unused ... │
└──────┴────────────────┴────────────────┘
 0      1                5                page_size
```

Allocating pops the free-list head (or grows the file if empty); freeing pushes a page onto the head. Both operations only ever touch this 5-byte prefix — the rest of a freed page's old content is left as-is until something else overwrites it.

## 4. Catalog page + collection slot

A catalog page holds a dense, forward-packed array of fixed-size collection slots — one entry per collection that currently exists (live or tombstoned).

**Page header (7 bytes):**

| Offset | Size | Field |
|---|---|---|
| 0 | 1 | `page_type` (`0x02`) |
| 1 | 4 | next catalog page, `0` = end of chain (`uint32`) |
| 5 | 2 | occupied slot count (`uint16`) |

**Collection slot (85 bytes), repeated `occupied` times starting right after the header:**

| Offset | Size | Field |
|---|---|---|
| 0 | 1 | flags (bit 0 = tombstone, bit 1 = internal collection) |
| 1 | 64 | name, zero-padded |
| 65 | 4 | head data-page pointer, `0` = collection has no pages yet (`uint32`) |
| 69 | 4 | tail data-page pointer (`uint32`) |
| 73 | 4 | page count (`uint32`) |
| 77 | 4 | doc count (`uint32`) |
| 81 | 4 | checksum, CRC32 over bytes `[0:81)` |

```
┌── page header (7B) ──┐┌─────────────────────── slot 0 (85B) ────────────────────────┐┌──────────────┐┌─────┐
│ type │ next │ occup. ││ flags │ name (64B, padded) │ head │ tail │ pgs │ docs │ CRC ││ slot 1 (85B) ││ ... │
└──────┴──────┴────────┘└───────┴────────────────────┴──────┴──────┴─────┴──────┴─────┘└──────────────┘└─────┘
```

Example — a freshly created collection named `"docs"`, no documents inserted yet:

```
flags=00  name="docs"+60×00  head=00000000  tail=00000000  pageCount=00000000  docCount=00000000  checksum=<CRC32 of the 81 bytes above>
```

`Head`/`Tail` stay `0` until the collection's first document is inserted — a collection is created without eagerly allocating any data page.

A slot's own index never changes once assigned: slot `i` always lives at byte offset `7 + i*85` on its page, so tombstoning a slot (setting bit 0 of `flags`) never requires moving any other slot.

## 5. Data page + slot

A data page holds actual documents (`_id` + TLV body, opaque to this package). Records grow forward from the header; the slot array grows **backward** from the end of the page — the two meet in the middle at whatever free space remains.

**Page header (13 bytes):**

| Offset | Size | Field |
|---|---|---|
| 0 | 1 | `page_type` (`0x03`) |
| 1 | 4 | next page in this collection's chain, `0` = end (`uint32`) |
| 5 | 2 | `free_start_offset` — where written records end (`uint16`) |
| 7 | 2 | `slot_count` — live + tombstoned slots (`uint16`) |
| 9 | 4 | `page_checksum` (`uint32`, CRC32 — see below for exact coverage) |

**Slot (9 bytes)**, slot `i` always at `page_size - (i+1)*9`, so slot 0 sits closest to the end of the page:

| Offset | Size | Field |
|---|---|---|
| 0 | 1 | flags (bit 0 = tombstone) |
| 1 | 2 | offset — where the record starts on this page (`uint16`) |
| 3 | 2 | length — record length in bytes (`uint16`) |
| 5 | 4 | checksum, CRC32 of the record's own bytes (`uint32`) |

Worked example, `page_size = 92`: three 12-byte records inserted, then the middle one deleted (tombstoned):

```
┌─────────────┬────────────┬────────────┬────────────┬────────────────┬─────────┬─────────┬─────────┐
│    header   │   A: 12B   │   B: 12B   │   C: 12B   │      free      │  slot2  │  slot1  │  slot0  │
│     13B     │   (live)   │   (tomb)   │   (live)   │      16B       │  → C    │  → B    │  → A    │
└─────────────┴────────────┴────────────┴────────────┴────────────────┴─────────┴─────────┴─────────┘
 0             13           25           37           49               65        74        83        92
```

`free_start_offset = 49` (end of the last written record); `slot_count = 3` (tombstoning never removes a slot entry, only sets its flag bit).

**`page_checksum` covers**, in this order: the header minus its own checksum field (bytes `[0:9)`), the entire slot array (`[65:92)` in the example above — including the tombstoned slot's entry, since its flags/offset/length are still live metadata), then each **live** record's bytes only (`A` and `C`, not `B`). Tombstoning a record changes only its slot's flag bit — never `page_checksum` — because the record's own bytes are deliberately excluded from the sum once tombstoned.

A document is directly addressable as `(page_number, slot_index)` and stays at that address for the page's lifetime, the same guarantee catalog slots give collections.

## 6. Rollback journal

A separate `<file>.journal`, present only while one write operation is in progress. Not a page-based format — a small header followed by full-page snapshots.

**Journal header (16 bytes):**

| Offset | Size | Field |
|---|---|---|
| 0 | 8 | magic bytes (`"ATLASJRL"`) — distinct from the `.db`'s own |
| 8 | 4 | record count (`uint32`) |
| 12 | 4 | header checksum, CRC32 over bytes `[0:12)` |

```
┌───────┬───────┬───────┐
│ magic │ count │ CRC32 │
└───────┴───────┴───────┘
 0       8       12      16
```

**Record**, one per distinct page touched so far this cycle, appended in the order pages were first modified:

| Offset | Size | Field |
|---|---|---|
| 0 | 4 | `page_number` (`uint32`) |
| 4 | `page_size` | `original_content` — the page's bytes before this cycle touched it |
| `4+page_size` | 4 | checksum, CRC32 over bytes `[0 : 4+page_size)` |

```
┌──────────┬────────────────────────────────────┬────────────┐
│ page_num │ original_content (page_size bytes) │   CRC32    │
└──────────┴────────────────────────────────────┴────────────┘
 0          4                                    4+page_size  8+page_size
```

The checksum covers `page_number` together with `original_content` — a torn write mid-append corrupting either field is caught by the same check. Only the *first* time a page is modified in a cycle gets a record; a page written to twice in one operation keeps its true pre-cycle content in the journal, not an intermediate state.
