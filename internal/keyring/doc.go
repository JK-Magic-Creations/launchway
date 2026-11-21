// Package keyring is Launchway's encryption at rest for the secrets the
// server itself stores.
//
// A value is sealed with a data key; the data key is stored wrapped by a
// key-encryption key; the key-encryption key is stored wrapped by the
// installation's root key, which is held OUTSIDE the database by a Holder.
// A copy of the database alone therefore holds no usable secret.
//
//	root key (a Holder: key file, environment, passphrase keystore, Vault or
//	          OpenBao transit; later a cloud KMS, PKCS#11 or a TPM)
//	  wraps  key-encryption keys (versioned, in table encryption_keks)
//	    wrap data keys (per purpose and organization, versioned, in table encryption_deks)
//	      seal values (AES-256-GCM; see format.go)
//
// Every sealed value is bound, by authenticated data, to the installation,
// its purpose, its organization, and the table, column and row it is stored
// in, so it cannot be moved to another row, column or organization and still
// open.
//
// The package imports no other Launchway package: the database is reached
// through the small DB interface, which the engine implements.
//
// Adding a root key holder: implement Holder (Wrap and Unwrap of a 32-byte
// key under additional authenticated data, and Status); nothing else in the
// package or its callers changes. A holder that keeps the root key inside a
// service or a device (cloud KMS, PKCS#11, TPM, transit) never needs the
// key in this process: Wrap and Unwrap are calls to it.
package keyring
