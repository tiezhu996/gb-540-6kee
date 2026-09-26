import type { ProposalState } from './enums/proposal-state'

// One referenced observation that no longer supports the proposal. The backend
// projects this from the current observation record but keeps the archived
// codes and replacement links for review traceability.
export interface InvalidEvidence {
  observation_id: number
  observation_code?: string
  reason: string
  parcel_id?: number
  state?: string
  replaced_by_id?: number | null
  replaced_by_code?: string
  quality_note?: string
}

export interface BoundaryProposal {
  id: number
  parcel_id: number
  base_version: number
  proposed_geojson: string
  observation_ids: number[] | string
  snap_tolerance_m: number
  area_delta_square_m: number
  proposal_state: ProposalState
  rationale: string
  version: number
  created_by: number
  reviewed_by?: number | null
  created_at: string
  updated_at: string
  invalid_evidence?: InvalidEvidence[]
}
