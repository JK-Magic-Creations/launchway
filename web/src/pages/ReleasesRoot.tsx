import { ReleasesPage, ReleasePage } from "./Releases";
import { ReleaseCalendarPage, ReleaseFormationsPage, ReleaseMatrixPage, ReleaseReportPage } from "./ReleasesManage";

/** The pages under /deployments/releases, chosen by the rest of the address, in one lazy chunk. */
export function ReleasesRoot({ a, b }: { a?: string; b?: string }) {
  if (!a) return <ReleasesPage />;
  if (b) return <ReleasePage project={a} version={b} />;
  switch (a) {
    case "matrix": return <ReleaseMatrixPage />;
    case "calendar": return <ReleaseCalendarPage />;
    case "formations": return <ReleaseFormationsPage />;
    case "report": return <ReleaseReportPage />;
  }
  return <ReleasesPage />;
}
