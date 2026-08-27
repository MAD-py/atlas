# internal/encoding — wire format

This document describes the exact bytes `Marshal`/`Unmarshal` produce and consume.

The format is a custom TLV (tag-length-value) binary codec: every value is stored as a one-byte tag identifying its type, optionally followed by a length and/or a payload. Arrays and objects can nest to any depth; nesting is only bounded by a document's overall size cap (one storage page's usable space, a few KB).

All multi-byte fixed-width fields (`float64`, `date`, `timestamp`) are **big-endian**. Byte values below are hex, most significant byte first, grouped for readability — they are not spaced in the actual file.

## 1. Document record layout

A document on disk is:

```
[ 12-byte id ][ fields ]
```

`fields` is a **flat, repeated sequence** of `(name, value)` pairs, with no wrapping tag or length prefix around the whole set. `Unmarshal` is handed exactly the byte range that belongs to one document, and decodes `(name, value)` pairs from it until that range is exhausted. Each pair is:

```
[ varint name length ][ name UTF-8 bytes ][ TLV value ]
```

A **nested** object field (a `map[string]any` value inside another value) uses the same `(name, value)*` sequence internally, but wrapped in a tag + length prefix — see §3.6. Only the document root omits that wrapper.

## 2. Type tags

| Tag    | Type          | Value bytes that follow the tag                          |
|--------|---------------|-----------------------------------------------------------|
| `0x00` | null          | *(none)*                                                   |
| `0x01` | bool `false`  | *(none — the tag itself is the value)*                     |
| `0x02` | bool `true`   | *(none — the tag itself is the value)*                     |
| `0x03` | int           | zigzag(n) as an unsigned LEB128 varint                     |
| `0x04` | float64       | 8 bytes, IEEE 754, big-endian                              |
| `0x05` | string        | varint byte length + UTF-8 bytes                           |
| `0x06` | array         | varint total payload length + TLV values back-to-back      |
| `0x07` | object        | varint total payload length + repeated `(name, value)` pairs |
| `0x08` | date          | 4 bytes, `int32` big-endian, days since 1970-01-01          |
| `0x09` | timestamp     | 8 bytes, `int64` big-endian, nanoseconds since epoch, UTC   |

## 3. Per-type byte trace

Each example shows the exact hex bytes a real call to `encodeValue` produces for that value, split into `[tag][...]` so you can see where each part starts.

### 3.1 null / bool

```
nil    -> 00
false  -> 01
true   -> 02
```

No value bytes — the tag alone fully determines the decoded result.

### 3.2 int

```
encodeValue(5)     -> 03 0A
                       │  └─ varint(zigzag(5))  = varint(10)  = 0A
                       └──── tag (int)

encodeValue(-100)  -> 03 C7 01
                       │  └──┴─ varint(zigzag(-100)) = varint(199) = C7 01
                       └──────── tag (int)
```

`-100` needs 2 varint bytes: zigzag(-100) = 199, which doesn't fit in a single 7-bit group (127 is the largest 1-byte varint value). See §4 for how negative numbers are mapped before varint-encoding.

### 3.3 float64

```
encodeValue(1.5) -> 04 3F F8 00 00 00 00 00 00
                     │  └────────────────────┴─ IEEE 754 bits of 1.5, big-endian
                     └──────────────────────────  tag (float64)
```

Always exactly 8 value bytes — the one fixed-width numeric type in the format (ints are varint-sized, floats are not).

### 3.4 string

```
encodeValue("Go") -> 05 02 47 6F
                      │  │  └──┴─ "Go" as UTF-8 ('G'=0x47, 'o'=0x6F)
                      │  └─────── varint byte length = 2
                      └────────── tag (string)
```

The length is a **byte** length, not a rune/character count — differs for multi-byte UTF-8 (e.g. "日" is 3 bytes, not 1).

### 3.5 array

```
encodeValue([1, true]) -> 06 03 03 02 02
                           │  │  └──┴──┴─ payload: encodeValue(1) ++ encodeValue(true)
                           │  │            = [03 02] ++ [02]
                           │  └─────────── varint payload length = 3
                           └────────────── tag (array)
```

The length prefix covers the whole payload (every element's TLV bytes concatenated), not a per-element count. Decoding reads TLV values from the payload until it's exhausted. Elements can be any mix of types; each is fully self-describing.

### 3.6 object (nested)

```
encodeValue({"x": 1}) -> 07 04 01 78 03 02
                          │  │  │  │  └──┴─ encodeValue(1) = 03 02
                          │  │  │  └─────── "x" as UTF-8 (0x78)
                          │  │  └────────── varint name length = 1
                          │  └───────────── varint payload length = 4
                          └──────────────── tag (object)
```

Same `(name, value)*` shape as the document root (§1), wrapped in a `tagObject` tag and payload-length prefix.

### 3.7 date

```
encodeValue(Date(1970-01-02)) -> 08 00 00 00 01
                                  │  └─────┴──┴─ int32 = 1 (day after epoch), big-endian
                                  └──────────────── tag (date)
```

The time-of-day component of whatever `time.Time` was wrapped in `Date(...)` is discarded before encoding — only the calendar day is stored.

### 3.8 timestamp

```
encodeValue(epoch + 1ns) -> 09 00 00 00 00 00 00 00 01
                             │  └────────────────────┴─ int64 = 1 (nanosecond), big-endian
                             └──────────────────────────  tag (timestamp)
```

Normalized to UTC before encoding (`t.UTC().UnixNano()`) — there is no stored offset/zone in v1, so a non-UTC `time.Time` round-trips to the same instant but loses its original zone.

## 4. Varint (LEB128) and zigzag

Every `int` value and every length prefix (string/array/object) is a **varint**: 7 payload bits per byte, low-order group first, with the top bit of each byte set on every byte except the last (the continuation bit). A value from 0-127 takes 1 byte; larger values take more:

```
300 encodes as: AC 02
  AC = 1_0101100   (continuation bit set, low 7 bits = 0101100)
  02 = 0_0000010   (continuation bit clear, next 7 bits = 0000010)
  reassembled:  0000010_0101100 = 100101100b = 300
```

Varints only encode unsigned integers. A signed `int64` is transformed into a `uint64` with **zigzag encoding** before being varint-encoded, interleaving the non-negative and negative ranges instead of placing them at opposite ends of the `uint64` space:

| `n` (int64) | `zigzag(n)` (uint64) |
|---|---|
| 0  | 0 |
| -1 | 1 |
| 1  | 2 |
| -2 | 3 |
| 2  | 4 |
| -3 | 5 |

`n >= 0` maps to `2n` (even); `n < 0` maps to `-2n - 1` (odd). The low bit of the result carries the sign: 0 for values that came from a non-negative `n`, 1 for values that came from a negative `n`.

Worked bit trace for `n = -2` (shown in 8 bits for readability; the real code operates on 64-bit values the same way):

```
n              = -2 = 1111 1110
n << 1         = -4 = 1111 1100   (shift left, 0 comes in at the bottom)
n >> 63        = -1 = 1111 1111   (arithmetic shift: sign bit smeared across every bit — a sign mask)
(n<<1)^(n>>63)      = 0000 0011 = 3   (matches the table: zigzag(-2) = 3)
```

Decoding reverses it: `u >> 1` recovers the magnitude, `u & 1` recovers the sign bit, `-int64(u & 1)` turns that into an all-0s or all-1s mask, and XOR-ing that mask against the magnitude undoes the complement applied on encode. See the doc comments on `zigzagEncode`/`zigzagDecode` in `varint.go` for the full bit-by-bit walkthrough.

## 5. Full worked example

Marshaling a document with id bytes `01 02 03 04 05 06 07 08 09 0A 0B 0C` and fields `{"ok": true}`:

```
01 02 03 04 05 06 07 08 09 0A 0B 0C 02 6F 6B 02
└───────── id (12 bytes) ─────────┘  │  │  │  └── tagTrue (value of "ok")
                                     │  └──┴───── "ok" as UTF-8
                                     └─────────── varint name length = 2
```

16 bytes total: 12 for the id, then the single `(name, value)` pair, with no wrapper around the fields region (per §1).
