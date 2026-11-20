# First-run setup

The first-run wizard is the quickest way from a new installation to a first project. It appears on
the Configuration page while the installation has no projects. It needs the admin role, which on a new
installation means signing in as one of the people named in `auth.admins` (or, with `auth.mode:
none`, anyone).

## What it asks

1. **What do you call things?** Launchway has four levels: installation, organisation, unit and
   project. Say what your organisation is called, and whether you want units (a group of projects with
   shared settings, such as a team or a department). If you say no, units stay out of every screen; you
   can turn them on later. You can also rename the levels, for example "Service" for project.
2. **How do people sign in?**
   - *No sign-in* is for trying Launchway on your own machine: everyone is an administrator. The
     wizard says so plainly and warns against it on a server others can reach.
   - *My company's identity provider* (OpenID Connect): the wizard asks where the provider is, the
     client ID you registered, which environment variable will hold the client secret, and which group
     are the first administrators. It tests the answers against the provider (it reads its discovery
     document and checks the redirect address) and writes the few lines for the server's configuration
     file. Sign-in stays in the file because the server needs it before it can read the database. The
     wizard never asks for the secret itself.
3. **Your first project.** The same steps as [Connect a repository](connect-repository.md).

## What it creates

A summary lists exactly what will be created: the level names in the installation settings, and the
project. Nothing is created until you confirm, and the wizard creates it all in one step or not at all;
cancelling at any step leaves nothing behind.

Changing the sign-in settings means editing the file and restarting the server; the wizard shows the
snippet with a copy button and says where it goes.

The same thing from the command line: `launchway config check-signin ...` prints the snippet;
`launchway config set installation installation -f levels.yaml` names the levels.
