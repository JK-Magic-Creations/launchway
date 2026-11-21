# Searching code

**Search** on a repository's page looks through its files at a branch, tag or commit. It is literal text by default; turn on **Regular expression** for a POSIX extended expression. Choose to look in file contents, in file names and paths, or both; narrow by folder and by extension (`go, ts`); turn on **Match case** if case matters. Each result shows the matching line with the match marked and links to the line in the file. The same search is `launchway repos search`, the HTTP route `GET /api/v1/repositories/{owner}/{name}/search` and the MCP tool `search_repository_code`.

## How it works, and what it will not do

Launchway has no search index and no separate search service. A search runs `git grep` over the repository's own copy at the commit you are looking at, so a result is never older than the last push and there is nothing to keep in step. In exchange it is bounded:

- at most 500 matches, 20 per file;
- 10 seconds, and 4 MiB of matches;
- 30 searches a minute for each person.

When a search stops at a bound the page says **Partial results** and why; a short answer is never silent. Narrow by folder or extension to see the rest. Binary files are skipped. On a repository of 10,000 files, a search for a word over all files takes about 40 ms and a file-name search about 120 ms.

The **go to file** finder (press `t` on a repository page) is a path search that ranks file names that start with what you typed first.

Searching across all repositories is not built yet.
