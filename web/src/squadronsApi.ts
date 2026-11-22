// Calls for Squadrons (stored as units) and Flights (stored as projects). They mirror internal/api/squadrons.go.
// The address words are the permanent names; the pages say Squadron and Flight through the terms helper.

import { call } from "./api";
import type { AttentionItem, HomeProject } from "./organizeApi";

export interface FlightRow extends HomeProject { pipelines: number }
export interface SquadronSummary { name: string; display_name: string; description?: string; flights: number; needs: number; failing: number; running: number }
export interface SquadronList { squadrons: SquadronSummary[]; unfiled: FlightRow[]; may_manage: boolean; why?: string }
export interface PersonRole { who: string; role: string }
export interface SettingCount { kind: string; path: string; title: string; count: number }
export interface Operation { flight: string; pipeline: string; last?: string; last_id?: string; last_number?: number; last_at?: number }
export interface SquadronPage {
  name: string; display_name: string; description?: string; flights: FlightRow[]; needs: AttentionItem[];
  people: PersonRole[]; settings: SettingCount[]; operations: Operation[]; operations_complete: boolean; may_manage: boolean; why?: string;
}
export interface Candidate { path: string; source: string; default_branch?: string; visibility?: string; address: string; flight?: string }
export interface Candidates { hosted: Candidate[]; hosted_without_flight: number; connections: string[]; connection_items: Candidate[]; connection_error?: string }
export interface PipelineProposal { state: "found" | "starter"; path?: string; starter?: string; note: string }
export interface AddedFlight { name: string; unit: string; pipeline: PipelineProposal }

export interface MoveEffect { kind: string; name: string; from: string; note?: string }
export interface MoveVariable { name: string; before: string; after: string }
export interface MoveSecret { store: string; before: string; after: string; result: "same" | "different" | "none" }
export interface MoveAccess { who: string; before: string; after: string; effect: "gains" | "loses" }
export interface MovePlan {
  flight: string; from: string; to: string; unchanged?: boolean; stops: MoveEffect[]; starts: MoveEffect[]; variables: MoveVariable[];
  secrets: MoveSecret[]; access: MoveAccess[]; references_from: "the pipeline file" | "assumed"; blocked: boolean; reasons: string[];
}
export interface MovePreview { to: string; plans: MovePlan[]; blocked: number }
export interface MoveResult { moved: string[]; blocked: MovePlan[]; unchanged: string[]; undo: Record<string, string>; to: string }

export interface OrganizeGroup { name: string; display_name?: string; description?: string; flights: string[] }
export interface OrganizeProposal { unfiled: string[]; groups: { name: string; flights: string[]; source: "folder" | "prefix" }[]; leftover: string[] }
export interface OrganizeResult extends MoveResult { created: string[]; undo_delete: string[] }

export const squadronsApi = {
  list: () => call<SquadronList>("GET", "/api/v1/units"),
  create: (name: string, display_name?: string, description?: string) => call<{ name: string }>("POST", "/api/v1/units", { name, display_name, description }),
  show: (name: string) => call<SquadronPage>("GET", `/api/v1/units/${encodeURIComponent(name)}`),
  candidates: (q: string, connection?: string) => call<Candidates>("GET", `/api/v1/project-candidates?q=${encodeURIComponent(q)}${connection ? `&connection=${encodeURIComponent(connection)}` : ""}`),
  addFlight: (unit: string, body: { repository?: string; repo?: string; name?: string; branch?: string }) =>
    call<AddedFlight>("POST", `/api/v1/units/${encodeURIComponent(unit)}/projects`, body),
  previewMove: (flights: string[], to: string) => call<MovePreview>("POST", "/api/v1/unit-moves/preview", { flights, to }),
  move: (flights: string[], to: string, comment?: string, homes?: Record<string, string>) => call<MoveResult>("POST", "/api/v1/unit-moves", { flights, to, comment, homes }),
  proposal: () => call<OrganizeProposal>("GET", "/api/v1/unit-organize"),
  organize: (groups: OrganizeGroup[]) => call<OrganizeResult>("POST", "/api/v1/unit-organize", { groups }),
  undoOrganize: (homes: Record<string, string>, delete_units: string[]) => call<MoveResult>("POST", "/api/v1/unit-organize/undo", { homes, delete_units }),
};
