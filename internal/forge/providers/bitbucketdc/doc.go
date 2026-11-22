// Package bitbucketdc is the provider for the host kind "bitbucket-datacenter":
// the self-managed Bitbucket product (Bitbucket Data Center, formerly Bitbucket
// Server), whose API is NOT the one of Bitbucket Cloud (kind "bitbucket"). It
// speaks only the REST calls named in the comments, each cited to the product's
// own reference: its OpenAPI file for version 10.2
// (https://developer.atlassian.com/server/bitbucket/10.2.swagger.v3.json, which
// the reference pages https://developer.atlassian.com/server/bitbucket/rest/
// are generated from; the operations, their permissions and their fields are
// read from it), the page on HTTP access tokens
// (https://confluence.atlassian.com/bitbucketserver/http-access-tokens-939515499.html),
// the webhook pages (https://confluence.atlassian.com/bitbucketserver/manage-webhooks-938025878.html
// and https://confluence.atlassian.com/bitbucketserver/event-payload-938025882.html).
// The reference pages themselves are scripts and cannot be fetched as text, so
// the OpenAPI file stands in for them; the other two are Atlassian's own pages.
//
// The install's address is required (it is self-managed, and may live below a
// context path): the REST API is at <base>/rest, and every call below uses the
// "latest" alias the reference names (/rest/api/latest/..., /rest/build-status/latest/...,
// /rest/branch-permissions/latest/..., /rest/git/latest/...). With the standard
// library, every request goes through the egress guard to the connection's own
// host only; no redirect is followed; the credential is read from its reference
// at the moment of a call and never stored, logged or returned.
//
// A repository is PROJECTKEY/slug; a personal repository has the project key
// ~USER (the tilde is kept as the host writes it). Bitbucket has no release
// object (tags only), so "releases.publish" is declared none, as for Bitbucket
// Cloud: the tag is made on the host and the release page stays in Launchway.
package bitbucketdc
