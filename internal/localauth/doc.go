// Package localauth is the built-in accounts of an installation: password
// hashing, the password policy, second factors (TOTP and recovery codes),
// the sign-in throttle and the one-time codes that start an installation or
// welcome a person. It holds no state and no database; the engine stores
// what it makes and the API calls it.
//
// What it follows: NIST SP 800-63B-4 (length over composition rules, a list
// of known-bad passwords, no forced rotation), OWASP's password storage
// guidance (Argon2id) and RFC 6238 (TOTP).
package localauth
