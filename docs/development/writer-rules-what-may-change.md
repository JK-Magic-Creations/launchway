# Writer rules that say what may change inside a file

**Status: design only.** Nothing on this page is built. It proposes how a pipeline writer rule could
grow from "which files" to "what in the file", for people deciding whether and how to build it.

## The problem

A pipeline writer rule of a repository (see [Write back to Git](/guide/gitops-write-back)) admits a
Flight's deploy steps to a protected branch for exactly the **files** they may write. That is a coarse
tool. A rule that admits `apps/shop/values.yaml` lets the step change anything in it: the image tag it
was meant to change, but also the replica count, a resource limit, an ingress host, or a new key
nobody expected. A review would catch that in a pull request, but in mode `direct` there is no review:
the rule is the only check. So people either admit a whole file and trust the step, or keep the file
out of reach and lose automatic Launches.

The same coarseness has a second edge. Many deployment repositories are read by a GitOps application
in the cluster. If a file the rule admits is a manifest, or a list of manifests, then whoever may write
the file may decide which objects that application creates, and some objects (a namespace, a role
binding, a custom resource definition, an admission webhook) reach beyond the namespace the Flight
belongs to. A rule that looks narrow ("one file") can be wide in effect.

## What a rule could say

A rule keeps its branch, its Flights and its files. A new optional part says **what may change in each
file**. Three kinds are enough to start with, chosen because each can be checked from the two versions
of the file alone, with no cluster:

1. **Image references only.** The change may alter the value of an image reference (the text after
   `image:` or in an `images:` list, including a tag or digest) and nothing else. This is the case
   almost every deploy step is.
2. **Named keys of a YAML document.** The change may alter the values at listed paths, such as
   `spec.template.spec.containers[*].image` or `replicaCount`, and nothing else. A path is written in
   a small, fixed notation (dots, `[*]`, `[name=web]`); there is no expression language.
3. **Anything in the file** (today's behavior). It stays the default for a rule that says nothing
   more, so no existing rule changes meaning.

The text a person types would read like the existing one: files, then what may change in each, for
example "may change only image references in `apps/shop/*.yaml`" or "may change only
`replicaCount` and `image.tag` in `apps/shop/values.yaml`".

## How the server would check it at receive time

The server already reads the files a push changes before it accepts the push. For a file with a
"what may change" part it would additionally:

1. Read the file as it is on the branch now and as the push would make it. Both are read from the
   repository's own objects; nothing is checked out and no step's word is taken.
2. Refuse a file that is not valid YAML in either version (when the rule names keys or images), saying
   which side failed. A file that cannot be understood cannot be shown to be safe.
3. Compare the two as **documents, not text**: parse both, remove the values the rule allows to
   change (replacing each with a marker), and require the remainders to be equal. Comments and
   formatting are ignored, so a re-indented file passes only if nothing else differs; added or removed
   keys and list items are changes unless the rule's path covers them.
4. For image references, require the new value to still look like an image reference (a name, then a
   tag or a digest) and, if the Flight's settings list the Depots it may use, to come from one of
   them.
5. Say what was wrong in plain words and in the same shape as today's refusals: "the pipeline of
   Flight shop may change only image references in apps/shop/values.yaml; this push also changes
   replicaCount (3 to 30)".

Limits worth stating: a multi-document YAML file is compared document by document, matched by kind and
name; a file that is not YAML (a template with directives, a binary) can only be admitted whole or
not at all; a push that renames or moves a file is judged as a deletion and an addition, so it is not
a way around the rule. The check is a bounded comparison of two small files, so it is cheap and runs
inside the existing time limit of a push.

## A warning for files that can reach beyond the Flight

When a rule is **saved**, the server would look at the files it admits on the branch as they are now
and say so when they are manifests (a document with `apiVersion` and `kind`) that the repository's
GitOps application may apply, and:

- any of them has a **cluster-scoped kind** (namespaces, cluster roles and bindings, custom resource
  definitions, storage classes, webhook configurations, priority classes), or
- the rule would admit changing a field that selects the target (`namespace`, a `kind`, a name).

The warning is not a refusal, because the people saving the rule may know their cluster. It says, in
the response and on the settings page: "This rule admits apps/platform/base.yaml. That file creates a
ClusterRole, which is not limited to one namespace. A pipeline allowed to change it can change what
the application grants across the cluster." It repeats in the Logbook entry of the rule, so a later
reader sees that it was shown. A rule that admits **only image references** does not get the warning
for the file's kinds, because it cannot change them; that is a reason to prefer the narrow form.

The warning needs to know which paths a GitOps application reads. The Flight's deployment settings
already name the application and its source path when one is connected; when none is, the check says
that it could not tell and does not guess.

## Why not something else

- **Require a pull request instead.** Mode `pull_request` already exists and is the right answer when
  a person should look. This note is for the cases where nobody will look and the rule must hold alone.
- **Let the step declare what it changes.** A step that is allowed to lie is not a check.
- **Run a policy engine in the receive path.** It would be more general and much heavier to run, test
  and explain. Three fixed kinds cover the Launches people actually do; a fourth can be added with its
  own tests when one is needed.

## Open questions

- Is a path notation with `[*]` and `[name=x]` enough, or are index-based paths needed for lists with
  no names?
- Should "image references only" be the default offered when a person adds a rule for a deploy step,
  with "anything in the file" requiring a deliberate choice and a reason in the Logbook?
- Should the cluster-scoped warning become a refusal for rules that admit anything-in-the-file on a
  manifest with cluster-scoped kinds, unless an administrator of the whole installation saves it?
