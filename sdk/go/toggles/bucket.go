package toggles

import (
	"crypto/sha256"
	"encoding/binary"
	"strconv"
)

// BucketCount is the number of buckets: weights are in hundredths of a percent.
const BucketCount = 10000

// Bucket returns the bucket, 0 to 9999, of a value for a toggle's salt. The
// hash, which every SDK repeats exactly:
//
//	SHA-256 of the UTF-8 text  salt + "." + value
//	take the first four bytes as an unsigned 32-bit number, big-endian
//	bucket = that number modulo 10000
//
// The value is the context attribute the rollout buckets by, as text: a
// string as it is, a whole number without a decimal point, any other number
// as the shortest text that reads back the same, a boolean as true or false.
// The same salt and value always give the same bucket, whatever the server,
// the language or the day, which is what keeps one person in one bucket as a
// percentage grows. A salt that is changed reshuffles everyone.
func Bucket(salt, value string) int {
	sum := sha256.Sum256([]byte(salt + "." + value))
	return int(binary.BigEndian.Uint32(sum[:4]) % BucketCount)
}

// bucketText renders a context value as the text that is hashed. ok is false
// for a value that cannot name a bucket (a list, a map, nothing).
func bucketText(v any) (string, bool) {
	switch x := Normalize(v).(type) {
	case string:
		return x, true
	case bool:
		return strconv.FormatBool(x), true
	case float64:
		if x == float64(int64(x)) && x > -1e15 && x < 1e15 {
			return strconv.FormatInt(int64(x), 10), true
		}
		return strconv.FormatFloat(x, 'g', -1, 64), true
	}
	return "", false
}

// pick returns the value of the bucket's share: buckets are laid end to end
// in the order written.
func (r *Rollout) pick(bucket int) any {
	edge := 0
	for _, b := range r.Buckets {
		edge += b.Weight
		if bucket < edge {
			return b.Value
		}
	}
	if n := len(r.Buckets); n > 0 {
		return r.Buckets[n-1].Value
	}
	return nil
}
