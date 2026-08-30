package file

import "os"

const (
	// dbFileMode matches the historical SQLite default: the owner can
	// read/write, everyone else can only read — future inspection tooling
	// over the current state of a .db needs nothing tighter.
	dbFileMode os.FileMode = 0o644

	// journalFileMode is tighter than dbFileMode: a journal record holds a
	// page's pre-modification bytes, which can outlive their own relevance
	// in the live .db (e.g. a value already overwritten there by the time
	// anyone looks), so it doesn't get the same broad readability.
	journalFileMode os.FileMode = 0o600
)

// OpenDB validates path (via DBPathFor) and opens the resulting .db file for
// read/write, creating it if it doesn't exist yet.
func OpenDB(path string) (*os.File, error) {
	resolved, err := DBPathFor(path)
	if err != nil {
		return nil, err
	}
	return os.OpenFile(resolved, os.O_RDWR|os.O_CREATE, dbFileMode)
}

// CreateJournal creates (truncating any stale leftover) dbFile's journal for
// read/write.
func CreateJournal(dbFile *os.File) (*os.File, error) {
	return os.OpenFile(
		JournalPathFor(dbFile.Name()),
		os.O_RDWR|os.O_CREATE|os.O_TRUNC,
		journalFileMode,
	)
}

// OpenJournalReadOnly opens dbFile's journal for reading only — e.g. so
// recovery can check whether one exists and replay it.
func OpenJournalReadOnly(dbFile *os.File) (*os.File, error) {
	return os.OpenFile(JournalPathFor(dbFile.Name()), os.O_RDONLY, 0)
}
