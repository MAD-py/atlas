package atlas

// Collection is a lightweight handle, not a cache of its catalog slot:
// Head/Tail/DocCount go stale the moment any document is inserted or
// deleted, so a slot must be re-resolved at the time of each operation
// rather than stored here.
type Collection struct {
	db   *DB
	name string
}
