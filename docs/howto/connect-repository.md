# Connect a repository

The wizard turns a repository into a Flight (codebase): Launchway watches it, starts runs when you say, and
shows each Sortie (run). Pipelines stay in Git: the wizard stores a reference to your repository, never a copy of
a pipeline.

## What it asks

1. **Where is the repository?** The address (https or ssh). There is nothing to connect per
   repository: the server reads repositories with its own Git credentials, one connection for every
   repository they may read (an ssh key or a credential helper of the account the server runs as, a
   token in a Git configuration). The wizard checks the address with those credentials and says in
   plain words if it cannot read it, for example that sign-in failed or the host cannot be reached.
2. **Which branch?** The wizard lists the branches it found and suggests the repository's default.
3. **What did it find?** It reads the pipeline file at the tip of the branch and shows the file found
   (`.launchway/pipeline.yaml`) and the commit. If the pipeline has mistakes, each one is listed with its
   file, line and commit, so you can fix it in Git; you can still continue.
4. **How do runs start?** Choose any of: on every push to chosen branches or tags; on pull requests
   (the wizard explains that pull-request code can come from anyone who can open one, so secrets are
   withheld by default); on a schedule written in the pipeline; by hand only. The polling interval is
   how often Launchway looks for new commits.
5. **Where does the pipeline come from?** Either the repository's own pipeline file (the default), or a
   *template* from a shared library: choose the library, the template and the version to pin it at (a
   tag, a branch or a commit; a tag is resolved to a commit and recorded on each Sortie). If the repository
   has no pipeline file yet, the wizard shows a small starter pipeline to copy into it, or you can write
   one in the editor, whose Save is a Git commit on a new branch.
6. **Cancel superseded runs?** Whether a new run of the same branch or pull request cancels older Sorties
   still going.

## What it creates

A summary lists the Flight exactly as it will be created. After you confirm it is created, and the
wizard offers to start a first Sortie and shows it.

If the repository is on your own network (a Git server at a private address) the server refuses to
contact it by default, so that nobody who can reach the server can use it to probe that network; the
wizard says so and offers an administrator to add the host to the allowed hosts. A path on the server
needs `egress.allow_local` in the bootstrap file (`make run` sets it). Do not put a user name or token
in the address: the server refuses it, because the configuration's history cannot be scrubbed; the server
uses its own Git credentials (an SSH key or a credential helper).

From the command line: `launchway config check-repository URL`, then `launchway Flights set NAME -f
project.yaml`.
