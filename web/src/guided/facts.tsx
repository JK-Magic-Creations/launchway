// The installation's state, loaded once for the whole shell and refreshed in
// the background: what the menu shows and what Home asks about come from
// the same real data.

import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from "react";
import { api, type Approval, type EnvironmentView, type Me, type Project, type RunSummary, type Runner } from "../api";
import { configApi, type LibraryInfo } from "../configApi";
import { NO_FACTS, type Facts } from "../nav/model";
import { applications, type AppRow } from "./derive";

export interface Installation {
  /** False until the first answer arrives. */
  loaded: boolean;
  projects: Project[];
  runs: RunSummary[];
  runners: Runner[];
  environments: EnvironmentView[];
  libraries: LibraryInfo[];
  applications: AppRow[];
  approvals: Approval[];
  facts: Facts;
  reload: () => void;
}

const EMPTY: Installation = {
  loaded: false, projects: [], runs: [], runners: [], environments: [], libraries: [], applications: [], approvals: [], facts: NO_FACTS, reload: () => {},
};

const Ctx = createContext<Installation>(EMPTY);

export function useInstallation(): Installation { return useContext(Ctx); }

/** What one request found: its answer, or nothing known (a person may not be allowed to see it). */
function settle<T>(p: Promise<T>): Promise<T | null> { return p.then((v) => v, () => null); }

export function InstallationProvider({ me, approvals, children }: { me: Me; approvals: Approval[]; children: ReactNode }) {
  const [data, setData] = useState<Omit<Installation, "facts" | "reload" | "applications" | "approvals">>({
    loaded: false, projects: [], runs: [], runners: [], environments: [], libraries: [],
  });
  const load = useCallback((background: boolean) => {
    Promise.all([
      settle(api.projects(background)),
      settle(api.runs({ limit: 50 }, background)),
      me.runners ? settle(api.runners(background)) : Promise.resolve(null),
      settle(api.environments(background)),
      settle(configApi.libraries()),
    ]).then(([projects, runs, runners, environments, libraries]) => {
      setData({ loaded: true, projects: projects ?? [], runs: runs ?? [], runners: runners ?? [], environments: environments ?? [], libraries: libraries ?? [] });
    });
  }, [me.runners]);
  useEffect(() => {
    load(false);
    const t = window.setInterval(() => load(true), 15000);
    return () => window.clearInterval(t);
  }, [load]);
  const reload = useCallback(() => load(true), [load]);

  const value = useMemo<Installation>(() => {
    const apps = applications(data.environments);
    const facts: Facts = data.loaded ? {
      projects: data.projects.length, runs: data.runs.length, runners: data.runners.length, environments: data.environments.length,
      applications: apps.length, libraries: data.libraries.length, approvals: approvals.length,
    } : { ...NO_FACTS, approvals: approvals.length };
    return { ...data, applications: apps, approvals, facts, reload };
  }, [data, approvals, reload]);
  return <Ctx.Provider value={value}>{children}</Ctx.Provider>;
}
