// Package sealing is the one place the server's stored secrets are sealed
// and opened, through the keyring (internal/keyring). Everything that keeps a
// secret in the database goes through a Service:
//
//   - SharedValue: the values every server of an installation agrees on
//     (signing and sealing keys, the shared runner token), in table meta;
//   - Box: one column of sealed values (SSH host keys, webhook secrets), each
//     bound to its row;
//   - Registry: the columns that hold secrets. A test walks the database's
//     schema and fails on a column that looks like it holds a secret and is
//     neither in the registry nor explained; another fails on code that
//     writes a registered column without sealing. New code that stores a
//     secret adds its column to the registry.
//
// A server with no root key runs in "not encrypted at rest" mode: it reads
// and writes as it always did and says so loudly, and the features that
// would add new secrets (Git over SSH, webhook secrets) refuse until one
// exists. Migrate seals what was stored in the clear, in place.
package sealing
