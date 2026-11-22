// Package azuredevops is the provider for the host kind "azuredevops": the
// cloud service (https://dev.azure.com/{organization}) and the self-managed
// server product (a base address that carries the collection, for example
// https://devops.example.com/tfs/DefaultCollection). It speaks only the REST
// calls named in the comments, each cited to the host's reference pages under
// https://learn.microsoft.com/en-us/rest/api/azure/devops/ (version 7.1, which
// the server product's reference also lists as "azure-devops-server-rest-7.1"
// with the address https://{instance}/{collection}/{project}/_apis/...), with
// the standard library, to the connection's own host only. Every request goes
// through the egress guard; no redirect is followed; the credential is read
// from its reference at the moment of a call and never stored, logged or
// returned.
//
// A repository is named organization/project/repository (on the server product
// the first element is the collection's name). The credential is a personal
// access token, sent as the password of HTTP Basic with an empty user name
// (https://learn.microsoft.com/en-us/azure/devops/organizations/accounts/use-personal-access-tokens-to-authenticate:
// "The username can be empty. Base64-encode the string :{PAT}").
//
// Azure DevOps has no release object (it has tags), so "releases.publish" is
// declared none: the annotated tag is made on the host, the release page and
// its files stay in Launchway, exactly as for the Bitbucket kind.
//
// Service hooks carry no signature. The webhook is verified by what the web
// hooks consumer documents (https://learn.microsoft.com/en-us/rest/api/azure/devops/hooks/consumers/list,
// the "webHooks" consumer's httpRequest action): basic authentication
// username and password, and HTTP headers. See events.go.
package azuredevops
