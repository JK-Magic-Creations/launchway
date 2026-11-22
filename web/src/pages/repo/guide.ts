// What the Guide says about the pages that read a repository's code. It lives with those pages, in their lazy
// chunk, so the first load does not carry it; opening any of them adds it before the Guide can be asked.
import { GUIDE } from "../../shell/guideData";
import { prose } from "../../prose";
const P = await prose("pages.repo.guide");

Object.assign(GUIDE, {
  "/repositories/:owner/:name/code": { title: "The code", text: P.t0 },
  "/repositories/:owner/:name/blame": { title: "Blame", text: P.t1 },
  "/repositories/:owner/:name/commits": { title: "Commits", text: P.t2 },
  "/repositories/:owner/:name/commit/:sha": { title: "A commit", text: P.t3 },
  "/repositories/:owner/:name/compare": { title: "Compare", text: P.t4 },
  "/repositories/:owner/:name/branches": { title: "Branches", text: P.t5 },
  "/repositories/:owner/:name/tags": { title: "Tags", text: P.t6 },
  "/repositories/:owner/:name/search": { title: "Search the code", text: P.t7 },
});
