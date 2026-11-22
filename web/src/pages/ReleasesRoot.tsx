import { ReleasesPage, ReleasePage, ReleaseGetPage } from "./Releases";
import { ReleaseCalendarPage, ReleaseFormationsPage, ReleaseMatrixPage, ReleaseReportPage } from "./ReleasesManage";

/** The pages under /deployments/releases, chosen by the rest of the address, in one lazy chunk. */
export function ReleasesRoot({ a, b, c }: { a?: string; b?: string; c?: string }) {
  if (!a) return <ReleasesPage />;
  if (b && c) return <ReleaseGetPage project={a} version={b} asset={c} />;
  if (b) return <ReleasePage project={a} version={b} />;
  switch (a) {
    case "matrix": return <ReleaseMatrixPage />;
    case "calendar": return <ReleaseCalendarPage />;
    case "formations": return <ReleaseFormationsPage />;
    case "report": return <ReleaseReportPage />;
  }
  return <ReleasesPage />;
}
