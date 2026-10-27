package agentdb

// Snapshot writes a consistent copy of the whole database (its WAL included) to path, which must
// not exist yet: SQLite's VACUUM INTO, safe while the host keeps writing.
func (d *DB) Snapshot(path string) error {
	_, err := d.db.Exec(`VACUUM INTO ?`, path)
	return err
}
