// Package ids generates identifiers that sort by creation time.
package ids

import (
	"crypto/rand"
	"encoding/base32"
	"time"
)

// The alphabet is in ascending ASCII order so that identifiers sort by time.
var enc = base32.NewEncoding("0123456789abcdefghjkmnpqrstvwxyz").WithPadding(base32.NoPadding)

// New returns "<prefix>_" followed by 20 characters: 48 bits of millisecond
// time and 48 random bits.
func New(prefix string) string {
	var b [12]byte
	ms := uint64(time.Now().UnixMilli())
	for i := 0; i < 6; i++ {
		b[i] = byte(ms >> (8 * (5 - i)))
	}
	if _, err := rand.Read(b[6:]); err != nil {
		panic(err)
	}
	return prefix + "_" + enc.EncodeToString(b[:])
}
