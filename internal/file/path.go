package file

import "strings"

const (
	dbExtension      = ".db"
	journalExtension = ".journal"
)

// DBPathFor validates path and returns the actual .db path to open:
// appends dbExtension if path doesn't already end with it. Anything beyond
// emptiness (a directory, a path with no filename component, ...) is left
// for the OS's own open() call to reject — it already does that correctly
// and more completely than a string-only check could.
func DBPathFor(path string) (string, error) {
	if path == "" {
		return "", ErrEmptyDatabasePath
	}
	if strings.HasSuffix(path, dbExtension) {
		return path, nil
	}
	return path + dbExtension, nil
}

// JournalPathFor derives the journal's path from the .db file's own path —
// a sibling file, same directory, ".journal" appended.
func JournalPathFor(dbPath string) string {
	return dbPath + journalExtension
}
