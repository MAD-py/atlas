package encoding

import "time"

// Date is a date-only value (no time component), stored as days since epoch.
// Distinct from time.Time so encodeValue can tell a date from a timestamp.
type Date time.Time

const secondsPerDay = 86400

// Time returns d as a time.Time at midnight UTC.
func (d Date) Time() time.Time { return time.Time(d) }

// String renders d as "2006-01-02".
func (d Date) String() string { return d.Time().Format("2006-01-02") }

// MarshalText implements encoding.TextMarshaler, rendering d the same way
// String does. Go doesn't let a defined type inherit its underlying type's
// methods, so without this encoding/json would reflect into Date's
// underlying time.Time's unexported fields and produce "{}" instead of a
// date string. Defined here rather than in the root atlas package because
// atlas.Date is a type alias for this type — Go forbids attaching methods to
// an aliased type from outside the package that defines it, so these methods
// have to live wherever Date itself is defined.
func (d Date) MarshalText() ([]byte, error) { return []byte(d.String()), nil }

// UnmarshalText is MarshalText's inverse.
func (d *Date) UnmarshalText(text []byte) error {
	t, err := time.Parse("2006-01-02", string(text))
	if err != nil {
		return err
	}
	*d = Date(t)
	return nil
}
