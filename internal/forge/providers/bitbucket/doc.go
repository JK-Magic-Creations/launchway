// Package bitbucket is the provider for the host kind "bitbucket": Bitbucket
// Cloud (https://bitbucket.org, API at https://api.bitbucket.org/2.0). It speaks only the
// REST calls named in the comments, each cited to the host's own OpenAPI file
// (https://api.bitbucket.org/swagger.json, which also names the OAuth scope
// each operation needs) and its documentation pages (the REST API
// introduction https://developer.atlassian.com/cloud/bitbucket/rest/intro/ and the
// webhook pages https://support.atlassian.com/bitbucket-cloud/docs/manage-webhooks/
// and https://support.atlassian.com/bitbucket-cloud/docs/event-payloads/), with the standard
// library, to the host's API address only. Every request goes through the egress guard;
// no redirect is followed; the credential is read from its reference at the moment of a
// call and never stored, logged or returned.
//
// Bitbucket Data Center (the self-managed product) has a different API (REST
// 1.0 under /rest/api/) and different webhooks; it is NOT covered: a base
// address other than https://bitbucket.org is refused with that message.
//
// Bitbucket has no release object (it has tags and "downloads"), so the
// capability "releases.publish" is declared none: the tag is made on the host,
// the release page and its files stay in Launchway.
package bitbucket
