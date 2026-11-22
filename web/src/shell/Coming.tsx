import { EmptyState, LinkButton, PageHead } from "../kit/layout";
import { Icon } from "../kit/Icon";
import { comingFor } from "./comingData";
import { DivPage } from "../kit/bits";

/** ComingPage is the honest page for a feature that does not exist yet: what it will do, what will appear
 * and what makes it appear. It shows no sample data. */
export function ComingPage({ path }: { path: string }) {
  const c = comingFor(path);
  if (!c) return <DivPage><EmptyState title="Nothing here" action={<LinkButton href="/" kind="pri">Go to Home</LinkButton>}>This page does not exist.</EmptyState></DivPage>;
  return (
    <div className="page coming">
      <PageHead eyebrow={c.area} title={c.title} badge={<span className="vchip"><Icon name="clock" size="s" />Coming</span>} sentence={c.does} />
      <section className="sheet pad coming-body">
        <h2 className="h-sm">What will appear here</h2>
        <p>{c.appears}</p>
        {c.action && <div className="acts"><LinkButton href={c.action.href} kind="pri" icon="arrow">{c.action.label}</LinkButton></div>}
        <p className="faint small coming-note">This page is not built yet, so it shows no sample numbers. It is here so you can see where it will live.</p>
      </section>
    </div>
  );
}
