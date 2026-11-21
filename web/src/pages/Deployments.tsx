import { EntityLink } from "../components/EntityLink";
import { Link } from "../components/ui";
import { WizardButton, WizardEmpty } from "../components/WizardButton";
import { needsLook } from "../guided/derive";
import { useInstallation } from "../guided/facts";

/** Deployments: what is running, where, and is it healthy. It lists what Launchway knows today: the
 * environments, and the Argo CD applications that deploy runners have seen. */
export function DeploymentsPage() {
  const inst = useInstallation();
  if (!inst.loaded) return <div className="page"><div className="loading">Loading…</div></div>;
  const look = inst.applications.filter(needsLook);
  return (
    <div className="page">
      <header className="page-head">
        <div>
          <h1>Deployments</h1>
          <p className="sub">What is running, where, and is it healthy.</p>
        </div>
        {inst.environments.length > 0 && <WizardButton wizard="add-environment" label="Add an environment" />}
      </header>

      {inst.environments.length === 0 ? (
        <WizardEmpty title="No environments yet" wizard="add-environment" label="Add an environment"
          text="An environment is a named place your software goes, like staging or production. A pipeline says deploy to staging; the environment says where that is. It takes one minute and deploys nothing." />
      ) : (
        <>
          <div className="guided-tiles">
            <Link href="/deployments/environments" className="guided-tile">
              <span className="guided-tile-n">{inst.environments.length}</span>
              <span>{inst.environments.length === 1 ? "environment" : "environments"}</span>
            </Link>
            {inst.applications.length > 0 && (
              <Link href="/deployments/applications" className="guided-tile">
                <span className="guided-tile-n">{inst.applications.length}</span>
                <span>{inst.applications.length === 1 ? "application" : "applications"}{look.length > 0 ? `, ${look.length} to look at` : ", all fine"}</span>
              </Link>
            )}
          </div>
          <section className="card" aria-labelledby="dep-env-h">
            <div className="card-head"><h2 id="dep-env-h" className="h-sm">Environments</h2><Link href="/deployments/environments">Open the environments</Link></div>
            <ul className="guided-list">
              {inst.environments.map((e) => (
                <li key={e.name}>
                  <EntityLink kind="environment" name={e.name} />
                  <span className="guided-grow faint">
                    {e.services.length === 0 ? "nothing deployed yet" : e.services.map((u) => u.service + (u.current ? ` ${u.current.version}` : "")).join(", ")}
                  </span>
                </li>
              ))}
            </ul>
          </section>
          {inst.applications.length === 0 && (
            <section className="card" aria-labelledby="dep-app-h">
              <h2 id="dep-app-h" className="h-sm">Argo CD applications</h2>
              <p className="faint">An application appears here once a deploy step has deployed it and a deploy runner has looked at it. Add a deploy step to a pipeline to see its application here.</p>
            </section>
          )}
        </>
      )}
    </div>
  );
}
