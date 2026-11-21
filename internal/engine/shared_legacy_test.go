package engine

// SharedValue is what releases before encryption at rest used to keep the
// values every server agrees on: a row of meta, in the clear. The server no
// longer has it (internal/sealing stores such values sealed); the test of
// servers starting together over one database still uses the row it wrote.
func (e *Engine) SharedValue(key, proposed string) (string, error) {
	unlock := e.writeLock()
	defer unlock()
	if _, err := e.db.Exec(`INSERT INTO meta(key, value) VALUES(?, ?) ON CONFLICT(key) DO NOTHING`, "shared:"+key, proposed); err != nil {
		return "", err
	}
	var v string
	err := e.db.QueryRow(`SELECT value FROM meta WHERE key = ?`, "shared:"+key).Scan(&v)
	return v, err
}
