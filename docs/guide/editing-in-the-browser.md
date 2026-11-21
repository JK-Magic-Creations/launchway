# Editing in the browser

Operators of a repository can change one file at a time from its page: **Edit** and **Delete** on an open file, **New file** in a folder. A save is a real commit with your message, made by the server for you, and it goes through the same branch rules, webhooks, push mirrors and pull-request updates as a push. It is the same path as `launchway repos put-file` and `delete-file`, the HTTP routes `PUT` and `DELETE /api/v1/repositories/{owner}/{name}/files` and the MCP tools `write_repository_file` and `delete_repository_file`.

The editor is a plain text box, saved exactly as typed. Leaving with unsaved text asks first.

## Where the commit goes

- **Commit directly** to the branch you are on. On a protected branch only the repository's administrators may; others are refused, and the page already says so.
- **Commit to a new branch and open a pull request**: Launchway makes the branch from where you are, commits there, and opens the new pull request page with both branches filled in. This is the way in for a protected branch.

A file changed by someone else while you edited is never overwritten: the save is refused with the file's new state, and you read it again. A mirror or an archived repository takes no changes. Every commit and every branch made or deleted is recorded in the Logbook (audit trail).

Not yet: uploading files, several files in one commit, cherry-pick, revert and patch download.
