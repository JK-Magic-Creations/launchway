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
2. **Your wing and its Squadrons.** Say what your organization is called, and whether you want
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

A summary lists exactly what will be created: the words in the Base settings, and the
project. Nothing is created until you confirm, and the wizard creates it all in one step or not at all;
cancelling at any step leaves nothing behind.

The same thing from the command line: `launchway config check-signin ...` checks the answers and
`launchway config set sign-in sign-in -f signin.yaml` saves them;
`launchway config set installation installation -f terms.yaml` chooses the words (a `terms:` entry, see [the words](../guide/words.md)).

## After the first administrator: the setup guide

Once the first administrator exists, the browser opens the **setup guide** (Settings, Setup guide). Each step can
be skipped and taken up again, each asks the server what can be done now (so a step that cannot work says why
instead of offering a button that fails), and each has a short note from the helper (Wingman, in the aviation
words) and a link into Learn.

1. **How people sign in.** Keep built-in accounts, or connect an identity provider. The provider is tested
   before anything is switched: you complete a real sign-in at the provider in a second window, and the result
   (who the provider said signed in, with which groups) is recorded in the Logbook. Nothing about how people
   sign in changes from the guide, because a server reads its provider only when it starts: after a good test the
   guide shows the exact setting to add and says that the built-in administrator stays as the way back in.
2. **Encryption and the root key.** What is protected, in plain words, and a guarded way to take a recovery copy
   of the root key: you type your password and a code from your authenticator again, choose a passphrase (typed
   twice), and receive a file in which the key is locked with that passphrase. Launchway records that a copy was
   taken and when, never the copy and never the passphrase.
3. **A runner.** Whether one is connected, and how to add another.
4. **Git hosting.** Turn it on (a restart is needed, because the server reads the setting only when it starts)
   and what Git over SSH needs.
5. **Bring in your repositories.** A server to import from (its address and token; the token is pasted once and
   kept sealed under the root key, and a server with no root key refuses the paste; or you name an environment variable), the repositories found,
   all or some, then each copied and compared with its source ref by ref, with a retry for any that failed.
   The same for a whole server from a script: `launchway repos import-all --connection NAME`.
6. **Your first pipeline**, from a starter, for one repository.
7. **Finish**: the plain security checklist (the same as `launchway doctor security`) and what to do next.
