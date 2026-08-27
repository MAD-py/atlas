package encoding

import (
	"bytes"
	"encoding/binary"
)

func writeUvarint(w *bytes.Buffer, v uint64) {
	var tmp [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(tmp[:], v)
	w.Write(tmp[:n])
}

// zigzagEncode maps a signed int64 to a uint64 so that small-magnitude
// values — positive OR negative — stay numerically small and therefore
// still encode as a short varint. Without this, a naive cast of a negative
// number to uint64 (two's complement) sets every high bit: -1 becomes
// 0xFFFFFFFFFFFFFFFF, which needs the full 10-byte varint no matter how
// "small" -1 conceptually is. Zigzag avoids that by interleaving the
// non-negative and negative ranges instead of placing them at opposite ends
// of the uint64 space:
//
//	n (int64)   zigzagEncode(n) (uint64)
//	 0      ->  0
//	-1      ->  1
//	 1      ->  2
//	-2      ->  3
//	 2      ->  4
//	-3      ->  5
//	...
//	 n>=0   ->  2*n        (even)
//	 n<0    ->  -2*n - 1   (odd)
//
// The formula `(n << 1) ^ (n >> 63)` computes that mapping with two bit
// operations instead of a branch:
//
//   - `n << 1` shifts every bit one place left, dropping the sign bit and
//     leaving a 0 in the low bit — for n>=0 this is already the final
//     answer (2*n); for n<0 it's the bit pattern that needs inverting.
//   - `n >> 63` is an *arithmetic* right shift, which replicates the sign
//     bit into every position: 0x0000000000000000 (all zero bits) when
//     n>=0, or 0xFFFFFFFFFFFFFFFF (all one bits) when n<0. It acts as a
//     sign mask, not as a shifted value.
//   - XOR-ing with an all-zero mask is a no-op (n>=0 case: result is just
//     n<<1). XOR-ing with an all-one mask flips every bit, i.e. computes
//     the one's complement (n<0 case) — which is exactly -2*n-1 in two's
//     complement arithmetic.
//
// Worked example with n = -2 (shown as an 8-bit two's complement pattern
// for readability; the real code operates on 64-bit values the same way):
//
//	n            = -2  = 11111110
//	n << 1       = -4  = 11111100   (top bit dropped, 0 shifted in at the bottom)
//	n >> 63      = -1  = 11111111   (arithmetic shift: sign bit smeared across every bit)
//	(n<<1)^(n>>63)      = 00000011  = 3   (matches the table above: zigzag(-2) = 3)
//
// See zigzagDecode for the inverse mapping, and writeUvarint for why keeping
// the encoded magnitude small matters.
func zigzagEncode(n int64) uint64 {
	return uint64((n << 1) ^ (n >> 63))
}

// zigzagDecode inverts zigzagEncode: given the interleaved uint64 (0, 1, 2,
// 3, 4, 5, ... representing 0, -1, 1, -2, 2, -3, ...), recover the original
// int64.
//
//   - `u >> 1` (logical shift, u is unsigned) discards the bit that
//     zigzagEncode used to record the sign, leaving the magnitude: for
//     u=2*n this is n; for u=2*n+1 (i.e. -2*n-1) this is n too, since
//     integer division rounds down — the sign bit carried no magnitude
//     information, it only needs to be reapplied next.
//   - `u & 1` recovers that sign bit back out: 0 for values that came from
//     a non-negative n, 1 for values that came from a negative n.
//   - `-int64(u & 1)` turns that into an all-zero mask (0 -> 0x000...000)
//     or an all-one mask (1 -> 0xFFF...FFF) — the same sign-mask trick
//     zigzagEncode uses, built by negation instead of a shift.
//   - XOR-ing the magnitude with that mask is a no-op when the mask is
//     zero, or flips every bit (one's complement) when the mask is all
//     ones — undoing exactly the complement zigzagEncode applied for
//     negative inputs.
//
// Worked example continuing from zigzagEncode's u = 3 (originally n = -2):
//
//	u            = 3  = 00000011
//	u >> 1       = 1  = 00000001
//	u & 1        = 1
//	-int64(u&1)  = -1 = 11111111
//	(u>>1) ^ (-1)      = 11111110 = -2   (back to the original n)
func zigzagDecode(u uint64) int64 {
	return int64(u>>1) ^ -int64(u&1)
}
