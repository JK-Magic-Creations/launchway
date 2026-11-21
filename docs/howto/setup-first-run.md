# First-run setup

The first-run wizard is the quickest way from a new installation to a first project. It appears on
the Configuration page while the installation has no projects. It needs the admin role, which on a new
installation means signing in as one of the people named in `auth.admins` (or, with `auth.mode:
none`, anyone).

## What it asks

1. **The words Launchway uses.** Choose the aviation words (Base, Wing, Squadron, Project; Wingman,
   Preflight, Clearance, Logbook) or the plain business words (Installation, Organization, Team,
   Project; Guide, Checks, Approval, Audit trail), watch the preview change, and rename any single word.
   They are labels only. See [the words Launchway uses](../guide/words.md).
2. **Your wing and its squadrons.** Say what your organization is called, and whether you want
   squadrons (a group of projects with shared settings, such as a team or a department). If you say
   no, squadrons stay out of every screen; you can turn them on later. The questions use the words you
   chose in the first step.
3. **How do people sign in?**
   - *No sign-in* is for trying Launchway on your own machine: everyone is an administrator. The
     wizard says so plainly and warns against it on a server others can reach.
   - *My company's identity provider* (OpenID Connect): the wizard asks where the provider is, the
     client ID you registered, which environment variable will hold the client secret, and which group
     are the first administrators. It tests the answers against the provider (it reads its discovery
     document and checks the redirect address) and saves the answers as the sign-in settings. The wizard
     never asks for the secret itself, only the name of the variable that holds it. Saving does not
     switch sign-in on: that needs a successful test sign-in by you, so that nobody is locked out, and
     this version cannot do the test yet. Until it can, the servers keep the sign-in they started with
     (the bootstrap file's, which is also the way back in if sign-in breaks), and the wizard says so.
     If your provider is on your own network, the server refuses to contact it until an administrator
     allows its host; the wizard offers to add it to the allowed hosts.
4. **Your first project.** The same steps as [Connect a repository](connect-repository.md).

## What it creates

A summary lists exactly what will be created: the words in the installation settings, and the
project. Nothing is created until you confirm, and the wizard creates it all in one step or not at all;
cancelling at any step leaves nothing behind.

The same thing from the command line: `launchway config check-signin ...` checks the answers and
`launchway config set sign-in sign-in -f signin.yaml` saves them;
`launchway config set installation installation -f terms.yaml` chooses the words (a `terms:` entry, see [the words](../guide/words.md)).
