import { Dialog } from "../kit/Dialog";
import { Icon } from "../kit/Icon";
import { onLink } from "../router";
import { visibleSections, visibleTabs, type Ctx } from "../nav/model";
import { SpanFaintSmall } from "../kit/bits";

/** Launcher is "All areas": every area with its one line of help and its pages. It complements the top
 * row (where hover help also lives) and is the phone's sixth tab. */
export function Launcher({ ctx, onClose }: { ctx: Ctx; onClose: () => void }) {
  const go = (e: React.MouseEvent<HTMLAnchorElement>) => { onLink(e); onClose(); };
  return (
    <Dialog label="All areas" onClose={onClose} className="launcher">
      <div className="sh-h">
        <h2>Everything in Launchway</h2>
        <div className="r">
          <span className="faint small hide-s">Ten areas. The first seven follow a change from code to running software.</span>
          <button type="button" className="iconbtn" onClick={onClose} aria-label="Close"><Icon name="x" /></button>
        </div>
      </div>
      <p className="lg-learn"><a href="/learn" onClick={go}><Icon name="book" size="s" />New here? Learn how Launchway works</a><SpanFaintSmall> The documentation of this release, in the product.</SpanFaintSmall></p>
      <div className="lg-grid">
        {visibleSections(ctx).map((s) => {
          const tabs = visibleTabs(s, ctx);
          return (
            <div className="lg-card" key={s.id}>
              <a className="lg-h" href={s.href} onClick={go}><Icon name={s.icon} size="l" /><b>{s.label}</b></a>
              <p>{s.promise}</p>
              {tabs.length > 1 && (
                <div className="lg-sub">
                  {tabs.map((t) => <a key={t.id} href={t.href} onClick={go}>{t.label}{t.soon ? " (soon)" : ""}</a>)}
                </div>
              )}
            </div>
          );
        })}
      </div>
    </Dialog>
  );
}

