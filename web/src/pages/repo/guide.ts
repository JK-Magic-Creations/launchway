// What the Guide says about the pages that read a repository's code. It lives with those pages, in their lazy
// chunk, so the first load does not carry it; opening any of them adds it before the Guide can be asked.
import { GUIDE } from "../../shell/guideData";

Object.assign(GUIDE, {
  "/repositories/:owner/:name/code": { title: "The code", text: "The files at a branch, tag or commit. Press t to find a file by name, j and k to move through the list." },
  "/repositories/:owner/:name/blame": { title: "Blame", text: "Who last changed each line. Blame before this change shows the file as it was before that commit." },
  "/repositories/:owner/:name/commits": { title: "Commits", text: "The history of a branch, or of one file or folder." },
  "/repositories/:owner/:name/commit/:sha": { title: "A commit", text: "What one change did, and the runs it started." },
  "/repositories/:owner/:name/compare": { title: "Compare", text: "How two branches differ, with a way to open a pull request." },
  "/repositories/:owner/:name/branches": { title: "Branches", text: "How far each branch is from the default; deleting one tells you how to bring it back." },
  "/repositories/:owner/:name/tags": { title: "Tags", text: "Names for commits that matter, such as a release." },
  "/repositories/:owner/:name/search": { title: "Search the code", text: "Find text in this repository's files. Results say when they are partial." },
});
