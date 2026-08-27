package encoding

import "time"

// Date is a date-only value (no time component), stored as days since epoch.
// Distinct from time.Time so encodeValue can tell a date from a timestamp.
type Date time.Time

const secondsPerDay = 86400
