package registry

import (
	"errors"
	"net/http"
)

// Error is a refusal in the vocabulary of the OCI Distribution API: a code
// clients know, a status, a plain sentence and (for a person) what to do.
type Error struct {
	Status  int
	Code    string
	Message string
	Next    string // what to do about it; "" when nothing helps
	Detail  any
}

func (e *Error) Error() string { return e.Message }

// The codes of the Distribution API, and Launchway's own after them.
const (
	CodeBlobUnknown         = "BLOB_UNKNOWN"
	CodeBlobUploadInvalid   = "BLOB_UPLOAD_INVALID"
	CodeBlobUploadUnknown   = "BLOB_UPLOAD_UNKNOWN"
	CodeDigestInvalid       = "DIGEST_INVALID"
	CodeManifestBlobUnknown = "MANIFEST_BLOB_UNKNOWN"
	CodeManifestInvalid     = "MANIFEST_INVALID"
	CodeManifestUnknown     = "MANIFEST_UNKNOWN"
	CodeNameInvalid         = "NAME_INVALID"
	CodeNameUnknown         = "NAME_UNKNOWN"
	CodeSizeInvalid         = "SIZE_INVALID"
	CodeUnauthorized        = "UNAUTHORIZED"
	CodeDenied              = "DENIED"
	CodeUnsupported         = "UNSUPPORTED"
	CodeTooManyRequests     = "TOOMANYREQUESTS"
	CodeTagInvalid          = "TAG_INVALID"
	CodeRangeInvalid        = "RANGE_INVALID"
	CodeTagImmutable        = "TAG_IMMUTABLE"
	CodeQuotaExceeded       = "QUOTA_EXCEEDED"
	CodeTooLarge            = "TOO_LARGE"
	CodeUnavailable         = "UNAVAILABLE"
)

func newErr(status int, code, msg, next string) *Error {
	return &Error{Status: status, Code: code, Message: msg, Next: next}
}

// asError turns any error into one a client is told: a registry Error as it
// is, anything else as a plain 500 that says nothing of the cause (the cause
// is logged by the caller).
func asError(err error) *Error {
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	return newErr(http.StatusInternalServerError, CodeUnavailable, "the registry could not do this; try again, and if it keeps failing tell an administrator", "")
}
