package atlas

import (
	"time"

	"github.com/MAD-py/atlas/internal/encoding"
)

// Date is a date-only value: no time-of-day component, unlike a plain
// time.Time field (which the TLV format calls a "timestamp"). It's a type
// alias for internal/encoding.Date, not a distinct type, so a Date passed
// into Document.Fields is already the exact type internal/encoding's TLV
// codec type-switches on — no conversion step anywhere. Because it's an
// alias, Date's Time/String/MarshalText/UnmarshalText methods are the ones
// defined on encoding.Date itself (Go doesn't allow attaching new methods to
// an aliased type from outside the package that defines it).
type Date = encoding.Date

// NewDate returns the Date for the given calendar day, discarding any
// time-of-day component.
func NewDate(year int, month time.Month, day int) Date {
	return Date(time.Date(year, month, day, 0, 0, 0, 0, time.UTC))
}
