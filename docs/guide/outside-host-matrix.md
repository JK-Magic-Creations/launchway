# What each outside host can do

<!-- Generated from the providers' own declarations. Do not edit by hand: run `make docs-matrix`. -->

Every host kind says for each feature whether it is **native** (the host does it), **emulated** (Launchway does it another way) or **none** (it cannot be done, with what to do instead). This page is generated from those declarations, so it cannot disagree with the product. The same data is served by `GET /api/v1/outside-host-matrix` and `launchway repos host matrix`. See [Keep your code on another host](outside-host.md) for connecting one.

## At a glance

| Feature | Azure DevOps | Bitbucket Cloud | Bitbucket Data Center | AWS CodeCommit | Forgejo | Gitea | GitHub | GitLab | Any Git server |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| Check who the token is | emulated | native | native | emulated | native | native | native | native | emulated |
| List and choose repositories | native | native | native | native | native | native | native | native | emulated |
| Create repositories | none | none | none | none | none | none | none | none | none |
| Read code | native | native | native | emulated | native | native | native | native | emulated |
| Start a Sortie on a push, tag or pull request | emulated | native | native | emulated | native | native | native | native | emulated |
| Check a person's access | native | native | native | native | native | native | native | native | native |
| Pull requests | native | native | native | native | native | native | native | native | none |
| Fetch a pull request head by a ref | none | none | none | none | native | native | native | native | none |
| Draft pull requests | native | native | native | none | none | none | native | none | none |
| Reviews | none | none | none | emulated | none | none | none | none | none |
| Report a result on a commit | native | native | native | none | native | native | native | native | none |
| Results that gate a merge | none | none | none | none | none | none | none | none | none |
| Merge a pull request | none | none | none | none | none | none | none | none | none |
| Merge when checks pass | native | none | native | none | native | native | native | native | none |
| Read branch rules | emulated | emulated | emulated | emulated | emulated | emulated | emulated | emulated | none |
| Forks | none | none | none | none | none | none | none | none | none |
| Large file storage | none | none | none | none | none | none | none | none | none |
| Write tags | native | native | native | emulated | native | native | native | native | native |
| Publish releases | none | none | none | none | native | native | native | native | none |
| Commit a file change | native | native | native | native | native | native | native | native | native |
| One updated summary comment on a pull request | native | native | native | native | native | native | native | native | none |

## Azure DevOps

Kind `azuredevops`.

Not native:

- **Check who the token is** (emulated): the host's reference has no call that names the owner of a personal access token (only a profile call on a separate host that a token with the scopes Launchway needs does not have), so the account is shown as the token itself and its use is proved by reading the allowed organizations. Instead: Nothing to do: the connection test names the token as "personal-access-token" and shows what it can read.
- **Create repositories** (none): a connection is narrow on purpose: it reads and writes repositories that exist and cannot create or delete them. Instead: Create the repository on Azure DevOps, then choose it in Launchway.
- **Start a Sortie on a push, tag or pull request** (emulated): service hooks carry no signature of the body: a delivery is authenticated by a secret that you put in the subscription as its basic-authentication password or in an HTTP header, which is a shared secret sent with every delivery and weaker than a signature (it does not cover the payload, and it is only as private as the HTTPS channel). Instead: Use an HTTPS address for the webhook, give the subscription the connection's webhook secret, and keep polling on (it is the fallback when a hook is missed); nothing in a payload is trusted without asking the host again.
- **Fetch a pull request head by a ref** (none): the reference names no ref that publishes a pull request's head; Launchway reads a pull request's head commit from the API. Instead: Nothing to do: Sorties for pull requests start from the commit the host names, and a pull request from a fork is read but its head is not fetched with the mirror.
- **Reviews** (none): reviews and votes stay on the host; Launchway shows a pull request and reports on it but does not vote on the host's behalf. Instead: Review and vote on Azure DevOps.
- **Results that gate a merge** (none): the host enforces required checks itself, in branch policies (a build validation or a status check on genre/name), and Launchway cannot change them. Instead: Add a status check policy for the status named launchway/launchway/ci (genre/name) to the branch on Azure DevOps.
- **Merge a pull request** (none): merging stays on the host, under its branch policies. Instead: Complete the pull request on Azure DevOps, or let Launchway set the host's auto-complete (write_back.merge: auto).
- **Read branch rules** (emulated): Launchway reads the host's branch policies but never changes them. Instead: Change branch policies on Azure DevOps.
- **Forks** (none): this is not built for this host yet. Instead: Do this on the Git host itself.
- **Large file storage** (none): this is not built for this host yet. Instead: Do this on the Git host itself.
- **Publish releases** (none): Git on Azure DevOps has no release object: it has tags (the product's "releases" belong to its pipelines, not to Git). Instead: Cut the release in Launchway: the annotated tag is made on the host, and the release page with its notes and files stays in Launchway, which serves the files.

What the host adds:

- **Auto-complete** (Launchway can switch it on): The host completes a pull request itself once every required policy passes; Launchway sets it on a write-back pull request (write_back.merge: auto) with the merge strategy of the Flight; cancelling it is done on the host.
- **Branch policies** (used on the host): The host can require a minimum number of approvers, named reviewers, a passing build validation or a status check before a pull request completes; set on the host, and Launchway reads them to refuse a direct write.
- **Required reviewers** (used on the host): The host adds reviewers to each pull request by path or rule and blocks completion without their approval; set up on the host.
- **Build validation** (used on the host): A branch policy can run a build for each pull request and block completion until it passes; Launchway's own statuses can stand in as a status check policy.
- **Statuses on the pull request** (used on the host): Each Sortie and failed step appears as a status on the commit and on the pull request, with a link back to the Sortie.

## Bitbucket Cloud

Kind `bitbucket`.

Not native:

- **Create repositories** (none): a connection is narrow on purpose: it reads and writes repositories that exist and cannot create or delete them. Instead: Create the repository on Bitbucket, then choose it in Launchway.
- **Fetch a pull request head by a ref** (none): the host publishes no ref for a pull request's head; Launchway reads a pull request's head commit from the API and fetches it by its hash. Instead: Nothing to do: Sorties for pull requests start from the commit the host names.
- **Reviews** (none): reviews stay on the host; Launchway shows a pull request and reports on it but does not review on the host's behalf. Instead: Review on Bitbucket.
- **Results that gate a merge** (none): the host enforces required builds itself, in a branch restriction, and Launchway cannot change them. Instead: Add a branch restriction that requires passing builds on Bitbucket.
- **Merge a pull request** (none): merging stays on the host, under its branch restrictions. Instead: Merge the pull request on Bitbucket.
- **Merge when checks pass** (none): the host's API has no call to switch auto-merge on for a pull request (its branch restriction "allow auto merge when builds pass" is a setting of the repository). Instead: Turn auto-merge on in the repository's branch restrictions on Bitbucket, or set write_back.merge to person and merge the pull request there.
- **Read branch rules** (emulated): Launchway reads the host's branch restrictions but never changes them. Instead: Change branch restrictions on Bitbucket.
- **Forks** (none): this is not built for this host yet. Instead: Do this on the Git host itself.
- **Large file storage** (none): this is not built for this host yet. Instead: Do this on the Git host itself.
- **Publish releases** (none): Bitbucket has no release object: it has tags and a list of downloadable files per repository, and neither carries release notes. Instead: Cut the release in Launchway: the tag is made on Bitbucket, and the release page with its notes and files stays in Launchway, which serves the files.

What the host adds:

- **Merge checks** (used on the host): The host can require passing builds, approvals, resolved comments and no requested changes before a pull request merges; set them in the repository's branch restrictions.
- **Default reviewers** (used on the host): The host adds reviewers to each new pull request by rule; set up on the host.
- **Builds on the pull request** (used on the host): Each Sortie and failed step appears as a build status on the pull request, with a link back to the Sortie.

## Bitbucket Data Center

Kind `bitbucket-datacenter`.

Not native:

- **Create repositories** (none): a connection is narrow on purpose: it reads and writes repositories that exist and cannot create or delete them. Instead: Create the repository on Bitbucket, then choose it in Launchway.
- **Fetch a pull request head by a ref** (none): the host's reference names no ref for a pull request's head; Launchway reads a pull request's head commit from the API and fetches it by its hash. Instead: Nothing to do: Sorties for pull requests start from the commit the host names.
- **Reviews** (none): reviews stay on the host; Launchway shows a pull request and reports on it but does not review on the host's behalf. Instead: Review on Bitbucket.
- **Results that gate a merge** (none): the host enforces required builds itself, as a merge check of the repository, and Launchway cannot change them. Instead: Add the "required builds" merge check on the repository's settings on Bitbucket.
- **Merge a pull request** (none): merging stays on the host, under its merge checks. Instead: Merge the pull request on Bitbucket, or let Launchway ask for the host's auto-merge (write_back.merge: auto).
- **Read branch rules** (emulated): Launchway reads the host's branch permissions and merge checks but never changes them. Instead: Change branch permissions and merge checks on Bitbucket.
- **Forks** (none): this is not built for this host yet. Instead: Do this on the Git host itself.
- **Large file storage** (none): this is not built for this host yet. Instead: Do this on the Git host itself.
- **Publish releases** (none): Bitbucket has no release object: it has tags, and a tag carries no release notes or files. Instead: Cut the release in Launchway: the tag is made on Bitbucket, and the release page with its notes and files stays in Launchway, which serves the files.

What the host adds:

- **Auto-merge** (Launchway can switch it on): The host merges a pull request itself once every merge check passes; Launchway asks for it on a write-back pull request (write_back.merge: auto), when the repository allows auto-merge in its settings.
- **Merge checks** (used on the host): The host can require approvals, successful builds and completed tasks before a pull request merges; set on the repository, and Launchway reads them to refuse a direct write.
- **Default reviewers** (used on the host): The host adds reviewers to each new pull request by rule; set up on the host.
- **Builds on the pull request** (used on the host): Each Sortie and failed step appears as a build status on the commit and its pull request, with a link back to the Sortie.

## AWS CodeCommit

Kind `codecommit`.

Not native:

- **Check who the token is** (emulated): Launchway calls no service that names the owner of an access key (it needs no extra permission for it), so the account is shown as the access key (masked) or the assumed role session, and its use is proved by listing the repositories. Instead: Nothing to do: the connection test shows the key or role session and what it can read.
- **Create repositories** (none): a connection is narrow on purpose: it reads and writes repositories that exist and cannot create or delete them. Instead: Create the repository in CodeCommit, then choose it in Launchway.
- **Read code** (emulated): the API lists branches and reads a commit but has no call that lists Git tags, and Launchway reads files and diffs from its own clone. Instead: Nothing to do: tags and files come from the clone Launchway keeps, and the branches and commits from the API.
- **Start a Sortie on a push, tag or pull request** (emulated): CodeCommit has no webhook to an address of your choice: its events leave through AWS's own event and notification services (EventBridge, SNS). Instead: Nothing to do: Launchway polls the repository and its pull requests (this is the mode of every connection of this kind). To react faster than the poll, route the host's events to a service of yours that calls Launchway's API.
- **Fetch a pull request head by a ref** (none): CodeCommit publishes no ref for a pull request's head; the head is the source branch, read from the pull request. Instead: Nothing to do: Sorties for pull requests start from the commit the host names for the source branch.
- **Draft pull requests** (none): CodeCommit pull requests have no draft state. Instead: Open the pull request when it is ready, or keep the branch without a pull request until then.
- **Reviews** (emulated): Launchway reads who approved a pull request (its approval states) but does not approve or comment as a reviewer on the host's behalf. Instead: Review and approve in the CodeCommit console or with the AWS CLI.
- **Report a result on a commit** (none): CodeCommit has no commit status or check API for another system to write. Instead: Launchway reports each result in the pull request's one summary comment, which it updates in place; a commit with no pull request has nowhere to say it and its result is read in Launchway.
- **Results that gate a merge** (none): CodeCommit's approval rules count approvals by people or roles, not results from another system, and Launchway cannot make a result a condition of merging. Instead: Give an approval rule to a role that reviews after the result, or gate the merge in Launchway's own pipeline.
- **Merge a pull request** (none): merging stays on the host, under its approval rules. Instead: Merge the pull request in CodeCommit.
- **Merge when checks pass** (none): CodeCommit has no auto-merge. Instead: Merge the pull request in CodeCommit once its approval rules are met (write_back.merge: person).
- **Read branch rules** (emulated): Launchway reads the approval rule templates associated with the repository but never changes them, and a template governs merging a pull request, not a push (a push is limited by IAM policy, which Launchway cannot read). Instead: Change approval rule templates in CodeCommit; limit direct pushes to a branch with an IAM policy.
- **Forks** (none): a CodeCommit pull request names one repository for its source and destination, so a pull request from a fork cannot exist and fork detection has nothing to detect. Instead: Nothing to do: every pull request of a repository comes from a branch of that repository.
- **Large file storage** (none): this is not built for this host yet. Instead: Do this on the Git host itself.
- **Write tags** (emulated): CodeCommit has no API call that creates a tag, so Launchway makes the annotated tag and pushes it with its own git, using the connection's credentials. Instead: Nothing to do, but the credentials need the GitPull and GitPush permissions on the repository (the API permissions alone do not tag).
- **Publish releases** (none): CodeCommit has no release object: it has branches and tags. Instead: Cut the release in Launchway: the annotated tag is made on the host, and the release page with its notes and files stays in Launchway, which serves the files.

What the host adds:

- **Approval rules** (used on the host): A pull request can need a number of approvals from named people or roles before it may be merged; the rules come from templates set on the host, and Launchway reads them and each pull request's approval states.
- **Approval rule templates** (used on the host): A template creates the same approval rule on every new pull request of the repositories it is associated with, for chosen destination branches; set up on the host.
- **Approval state** (used on the host): Who approved or revoked their approval of the current revision of a pull request, read from the host and shown on the Flight.
- **IAM access control** (used on the host): Who may read, push, tag or merge is decided by IAM policy on the repository (and a role Launchway assumes for a short session), not by a token's scopes.

## Forgejo

Kind `forgejo`.

Not native:

- **Create repositories** (none): a connection is narrow on purpose: it reads and writes repositories that exist and cannot create or delete them. Instead: Create the repository on the Git host, then choose it in Launchway.
- **Draft pull requests** (none): the host's API has no draft option when creating or editing a pull request, so Launchway reads a draft but does not make or change one. Instead: Mark the pull request as draft or ready on the Git host.
- **Reviews** (none): reviews stay on the host; Launchway shows a pull request and reports on it but does not review on the host's behalf. Instead: Review on the Git host.
- **Results that gate a merge** (none): the host enforces required status checks itself, in its branch protection, and Launchway cannot change them. Instead: Mark the Launchway status contexts as required in the host's branch protection.
- **Merge a pull request** (none): merging stays on the host, under its branch protection. Instead: Merge the pull request on the Git host.
- **Read branch rules** (emulated): Launchway reads the host's branch protection but never changes it. Instead: Change branch protection on the Git host.
- **Forks** (none): this is not built for this host yet. Instead: Do this on the Git host itself.
- **Large file storage** (none): this is not built for this host yet. Instead: Do this on the Git host itself.

What the host adds:

- **Merge when checks succeed** (Launchway can switch it on): The host merges a pull request by itself once its required checks pass (a scheduled merge); Launchway schedules it for a write-back pull request (write_back.merge: auto) and can cancel it.
- **Branch protection rules** (used on the host): The host enforces who may push or merge, required approvals and required status checks; Launchway reads them to refuse a direct write and its statuses can be made required.
- **Statuses on the pull request** (used on the host): Each Sortie and failed step appears as a commit status on the pull request, with a link back to the Sortie.

## Gitea

Kind `gitea`.

Not native:

- **Create repositories** (none): a connection is narrow on purpose: it reads and writes repositories that exist and cannot create or delete them. Instead: Create the repository on the Git host, then choose it in Launchway.
- **Draft pull requests** (none): the host's API has no draft option when creating or editing a pull request, so Launchway reads a draft but does not make or change one. Instead: Mark the pull request as draft or ready on the Git host.
- **Reviews** (none): reviews stay on the host; Launchway shows a pull request and reports on it but does not review on the host's behalf. Instead: Review on the Git host.
- **Results that gate a merge** (none): the host enforces required status checks itself, in its branch protection, and Launchway cannot change them. Instead: Mark the Launchway status contexts as required in the host's branch protection.
- **Merge a pull request** (none): merging stays on the host, under its branch protection. Instead: Merge the pull request on the Git host.
- **Read branch rules** (emulated): Launchway reads the host's branch protection but never changes it. Instead: Change branch protection on the Git host.
- **Forks** (none): this is not built for this host yet. Instead: Do this on the Git host itself.
- **Large file storage** (none): this is not built for this host yet. Instead: Do this on the Git host itself.

What the host adds:

- **Merge when checks succeed** (Launchway can switch it on): The host merges a pull request by itself once its required checks pass (a scheduled merge); Launchway schedules it for a write-back pull request (write_back.merge: auto) and can cancel it.
- **Branch protection rules** (used on the host): The host enforces who may push or merge, required approvals and required status checks; Launchway reads them to refuse a direct write and its statuses can be made required.
- **Statuses on the pull request** (used on the host): Each Sortie and failed step appears as a commit status on the pull request, with a link back to the Sortie.

## GitHub

Kind `github`.

Not native:

- **Create repositories** (none): a connection is narrow on purpose: it reads and writes repositories that exist and cannot create or delete them. Instead: Create the repository on the Git host, then choose it in Launchway.
- **Reviews** (none): reviews stay on the host; Launchway shows a pull request and reports on it but does not review on the host's behalf. Instead: Review on the Git host.
- **Results that gate a merge** (none): the host enforces required checks itself, in its branch protection, and Launchway cannot change them. Instead: Mark the Launchway status contexts as required in the host's branch protection settings.
- **Merge a pull request** (none): merging stays on the host, under its branch protection. Instead: Merge the pull request on the Git host.
- **Read branch rules** (emulated): Launchway reads the host's branch protection but never changes it. Instead: Change branch protection on the Git host.
- **Forks** (none): this is not built for this host yet. Instead: Do this on the Git host itself.
- **Large file storage** (none): this is not built for this host yet. Instead: Do this on the Git host itself.

What the host adds:

- **Auto-merge** (Launchway can switch it on): The host merges a pull request by itself once its required reviews and checks pass; Launchway switches it on for a write-back pull request (write_back.merge: auto), so a deploy's pull request lands with no one waiting on it.
- **Merge queue** (used on the host): The host can queue approved pull requests and test them together before they land; set it up in the host's branch protection.
- **Required reviews and code owners** (used on the host): The host enforces who must approve a change (code owners, required approvals); Launchway's statuses can be made required alongside them.
- **Checks on the pull request** (used on the host): Each Sortie and failed step appears as a status on the pull request's checks, with a link back to the Sortie.

## GitLab

Kind `gitlab`.

Not native:

- **Create repositories** (none): a connection is narrow on purpose: it reads and writes projects that exist and cannot create or delete them. Instead: Create the project on the Git host, then choose it in Launchway.
- **Draft pull requests** (none): the host's API documentation for creating and updating a merge request has no draft parameter, so Launchway reads a draft but does not make or change one. Instead: Mark the merge request as draft or ready on the Git host.
- **Reviews** (none): reviews stay on the host; Launchway shows a merge request and reports on it but does not review on the host's behalf. Instead: Review on the Git host.
- **Results that gate a merge** (none): the host enforces required pipelines and approvals itself, in the project's merge request settings and protected branches, and Launchway cannot change them. Instead: Require the Launchway status name in the project's merge request settings on the Git host.
- **Merge a pull request** (none): merging stays on the host, under its protected branches and approval rules. Instead: Merge the merge request on the Git host.
- **Read branch rules** (emulated): Launchway reads the host's protected branch but never changes it. Instead: Change protected branches on the Git host.
- **Forks** (none): this is not built for this host yet. Instead: Do this on the Git host itself.
- **Large file storage** (none): this is not built for this host yet. Instead: Do this on the Git host itself.

What the host adds:

- **Auto-merge** (Launchway can switch it on): The host merges a merge request by itself once its checks pass (its auto-merge, formerly merge when pipeline succeeds); Launchway switches it on for a write-back merge request (write_back.merge: auto), so a deploy's merge request lands with no one waiting on it.
- **Merge trains** (used on the host): The host can queue merge requests in a merge train and test each with those ahead of it before it lands; set it up on the host.
- **Approval rules** (used on the host): The host enforces who must approve a merge request (approval rules, code owners); Launchway's statuses can be required alongside them.
- **Statuses on the merge request** (used on the host): Each Sortie and failed step appears as a commit status on the merge request, with a link back to the Sortie.

## Any Git server

Kind `plain`.

Not native:

- **Check who the token is** (emulated): Git has no call that says who a credential is, so the account is the user name the connection sends; its use is proved against a repository (git ls-remote) when one is chosen. Instead: Nothing to do: choose a repository, and Launchway proves the credential by reading it.
- **List and choose repositories** (emulated): Git cannot list the repositories of a server, so there is nothing to search or choose from. Instead: Enter the repository's path (owner/name, as it is in the server's clone address); Launchway proves it with git ls-remote and reads its default branch.
- **Create repositories** (none): a connection reads and writes repositories that exist; Git cannot create or delete one on a server. Instead: Create the repository on the Git server, then enter its path in Launchway.
- **Read code** (emulated): Git lists a repository's branches and tags (ls-remote), but a commit, a file or a diff is read from Launchway's own clone. Instead: Nothing to do: files and history come from the clone Launchway keeps.
- **Start a Sortie on a push, tag or pull request** (emulated): a plain Git server sends no webhook of a format Launchway can check, and has no pull requests to follow. Instead: Nothing to do: Launchway polls the repository on its schedule and starts a Sortie on a push. To react at once, call the Flight's git-hook address from the server's post-receive hook (see the guide).
- **Pull requests** (none): a plain Git server has no pull requests. Instead: For write-back, push to a branch (write_back mode direct to a branch the credential may push to) and ask a person to merge it on the server; or connect a host that has pull requests.
- **Fetch a pull request head by a ref** (none): a plain Git server publishes no ref for a pull request, because it has none. Instead: Nothing to do: Sorties start from pushes to branches and tags.
- **Draft pull requests** (none): a plain Git server has no API Launchway can call, only Git itself. Instead: Do this on the Git server itself, or connect a host Launchway has a provider for.
- **Reviews** (none): a plain Git server has no reviews. Instead: Review in whatever process your team uses; Launchway does not hold reviews for this server.
- **Report a result on a commit** (none): a plain Git server has no commit status or check, and no pull request to carry a comment. Instead: Results are shown in Launchway only (the Flight's Sorties); nothing is written back to the server.
- **Results that gate a merge** (none): a plain Git server has no API Launchway can call, only Git itself. Instead: Do this on the Git server itself, or connect a host Launchway has a provider for.
- **Merge a pull request** (none): there are no pull requests to merge. Instead: Merge on the server with Git, or in Launchway's own forge if the code is hosted there.
- **Merge when checks pass** (none): a plain Git server has no API Launchway can call, only Git itself. Instead: Do this on the Git server itself, or connect a host Launchway has a provider for.
- **Read branch rules** (none): Git cannot tell Launchway which branches the server protects, so a refusal can only come from the push itself. Instead: Nothing to do: a push the server's hook refuses fails with the server's own message, which Launchway returns.
- **Forks** (none): a plain Git server has no forks or pull requests from them. Instead: Nothing to do.
- **Large file storage** (none): a plain Git server has no API Launchway can call, only Git itself. Instead: Do this on the Git server itself, or connect a host Launchway has a provider for.
- **Publish releases** (none): a plain Git server has tags but no release object. Instead: Cut the release in Launchway: the annotated tag is pushed to the server, and the release page with its notes and files stays in Launchway, which serves the files.
- **One updated summary comment on a pull request** (none): a plain Git server has no pull request to hold a summary comment. Instead: Read each Sortie's summary in Launchway.

What the host adds:

- **Any Git server** (used on the host): Any server that speaks Git over HTTPS, with a token or a user name and password; Launchway checks out, follows pushes by polling, pushes tags and commits files through Git itself.
