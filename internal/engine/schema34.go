package engine

import (
	"strings"

	"github.com/jk-magic-creations/launchway/internal/keyring"
)

// Schema 34, encryption at rest (internal/keyring), kept in a file of its
// own: db.go is over the size ratchet.

// schemaEncryption is the schema that brought the key tables.
const schemaEncryption = 34

func init() {
	if len(migrations) != schemaEncryption-1 {
		panic("the encryption migration (34) must follow migrations 1 to 33")
	}
	migrations = append(migrations, encryptionMigration)
}

// 34: encryption at rest.
//
//	encryption_state holds small facts (the installation id, the progress of
//	sealing what was stored in the clear); encryption_keks the
//	key-encryption keys, each wrapped by the installation's root key, which
//	is held outside the database; encryption_deks the data keys per purpose
//	and organization, each wrapped by a key-encryption key. Nothing in them
//	is usable without the root key.
//	Compatibility: additive: new tables and nothing else. A server of an
//	older schema ignores them. Do not run an older server beside newer ones
//	once a root key is in use: it would not understand the sealed values.
//	Downgrade: nothing to undo; with no root key configured every secret is
//	read and written as before.
var encryptionMigration = migration{additive, strings.TrimSpace(keyring.SchemaSQL)}
