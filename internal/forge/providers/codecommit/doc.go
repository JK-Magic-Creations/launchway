// Package codecommit is the provider for the host kind "codecommit": AWS
// CodeCommit, the cloud provider's Git service. It speaks only the calls named
// in the comments, each cited to the service's API reference
// (https://docs.aws.amazon.com/codecommit/latest/APIReference/) or its user
// guide (https://docs.aws.amazon.com/codecommit/latest/userguide/), with the
// standard library and the shared request signer internal/awssig (no AWS SDK),
// to the connection's own hosts only. Every request goes through the egress
// guard; no redirect is followed; the credentials are read from their
// reference at the moment of a call and never stored, logged or returned.
//
// Protocol. The API is JSON 1.1 over HTTPS: a POST to / with the headers
// X-Amz-Target: CodeCommit_20150413.<Action> and Content-Type:
// application/x-amz-json-1.1, signed with Signature Version 4 for the service
// "codecommit" (the sample requests of each API page show exactly these
// headers). Git over HTTPS is signed too: see git.go.
//
// A connection. The base address names the region and the Git endpoint:
// https://git-codecommit.<region>.amazonaws.com (the "Git connection
// endpoints" table of https://docs.aws.amazon.com/codecommit/latest/userguide/regions.html);
// the API endpoint codecommit.<region>.amazonaws.com
// (https://docs.aws.amazon.com/general/latest/gr/codecommit.html) and the
// token service endpoint sts.<region>.amazonaws.com
// (https://docs.aws.amazon.com/IAM/latest/UserGuide/id_credentials_temp_enable-regions.html)
// are derived from it, so no new connection field and no migration is needed.
// The credential reference names one secret holding a small JSON document
// (see creds.go): an access key pair, optionally a session token, optionally a
// role to assume with the key pair.
//
// A repository is named account/name: the 12-digit account id (the owners
// list names accounts) and the repository name, which is unique in a region of
// an account.
//
// CodeCommit sends no webhook to an address of your choice, has no commit
// status, no release object, no tag API and no fork: each is declared
// honestly in Capabilities, with the way around it.
package codecommit
