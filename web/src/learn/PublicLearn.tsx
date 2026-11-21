import { Icon } from "../kit/Icon";
import { onLink, usePath } from "../router";
import { useFocusableScrollers } from "../shell/scrollers";
import { LearnRoot } from "./Learn";

/** Learn for someone who is not signed in: the same pages, in a bare frame with a way to sign in. */
export function PublicLearn() {
  const path = usePath();
  useFocusableScrollers();
  return (
    <>
      <a href="#main" className="skip-link" onClick={(e) => { e.preventDefault(); document.getElementById("main")?.focus(); }}>Skip to the page</a>
      <header className="top">
        <div className="top-in">
          <a className="brand" href="/learn" onClick={onLink} aria-label="Learn Launchway"><Icon name="logo" size="xl" /><span className="bn">Launchway</span></a>
          <div className="top-r">
            <a className="btn btn-sm btn-primary" href={"/auth/login?next=" + encodeURIComponent("/")}>Sign in</a>
          </div>
        </div>
      </header>
      <main className="main" id="main" tabIndex={-1}><LearnRoot path={path} /></main>
    </>
  );
}
