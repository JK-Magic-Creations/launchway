import { useEffect, useState } from "react";
import { forgeApi, type HostedRepo } from "../forgeApi";

/** The repositories Launchway hosts that the person may read, or null until known (and when hosting is off or the call fails). */
export function useHostedRepos(): HostedRepo[] | null {
  const [repos, setRepos] = useState<HostedRepo[] | null>(null);
  useEffect(() => {
    let live = true;
    forgeApi.repositories().then((r) => live && setRepos(r), () => live && setRepos(null));
    return () => { live = false; };
  }, []);
  return repos;
}

/** Where to browse a hosted repository. */
export const hostedHref = (r: HostedRepo) => `/repositories/${encodeURIComponent(r.home ?? "")}/${encodeURIComponent(r.name)}`;
