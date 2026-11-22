# Template repositories

A **template repository** is a repository people start new repositories from. The new repository gets the
template's files as **one new commit with no history**, optionally with a few values filled in. Everything
below works the same in the HTTP API, with the MCP tools and with `launchway repos template`.

Git hosting must be on (`forge.hosting`).

## Marking a template

An administrator of the repository marks it:

```sh
launchway repos fork-settings acme/starter --template=true
```

(`PATCH /api/v1/repositories/{owner}/{name}` with `{"template": true}`.) Unmark it with `--template=false`.

## What a template asks for

A template may declare a few variables in `.launchway/template.yaml` on its default branch:

```yaml
variables:
  - name: project                 # lower case letters, digits and underscores, up to 31 characters
    description: The name of the project
    pattern: '[a-z][a-z0-9-]{1,30}'   # the whole value must match; Go regular expression syntax
  - name: author
    description: Who wrote it
    default: Anonymous
contents: ["**/*.md", "src/**"]   # the files whose text is filled in; empty means every text file
names: true                       # fill in file and folder names too (default false)
```

At most 20 variables; a value is at most 100 characters and holds no line breaks or other control
characters. When `names` is on, a value must also be a plain name: no `/`, no `\`, no `..`.

Substitution is **literal replacement of the declared tokens**: every `{{project}}` becomes the value, and
nothing else is touched (`{{ project }}` with spaces, or `{{other}}`, stays as written). Nothing is evaluated, no
command runs, no value is ever read as a path or an expression. Binary files, symbolic links and files over
1 MiB are copied as they are. The manifest itself is left out of the new repository. A path the new repository
could not hold (one that leaves it, or names Git's own directory) is refused before anything is stored.

## Using a template

```sh
launchway repos template show acme/starter
launchway repos template use acme/starter --name billing --var project=billing --var author=Ada
launchway repos template use acme/starter --name billing --owner team --all-branches --visibility private
```

You need read access to the template and the admin role on the path of the new repository, as for a
[fork](forks.md#making-a-fork). The new repository is never more visible than the template. By default
only the default branch comes, as one commit by you with the message *Initial commit from the template
OWNER/NAME*; with `--all-branches` every other branch comes too (up to 25), each as one commit with no
history. A value that fails its pattern, a variable with no value and no default, or a name the template
does not declare is refused (`400`), and a refused use leaves no repository behind.

A template can carry a pipeline file (`.launchway/pipeline.yaml`), owners files and anything else a repository
has, so a new repository arrives with a working path to production. It cannot carry an Airfield setup yet.

## Reference

- API: `GET /api/v1/repositories/{owner}/{name}/template`, `POST .../generate`
  (see the [HTTP API](../reference/http-api.md)).
- MCP: `get_template`, `use_template`, `fork_settings` (with `template`).
- Logbook: `repository.from_template`.
- Not built yet: the "Use this template" button in the web interface.
