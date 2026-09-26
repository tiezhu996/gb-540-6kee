import type { ProposalState } from './enums/proposal-state'

export interface InvalidObservation {
  observation_id: number
  observation_code: string
  state: string
  reason: string
  replaced_by_id?: number
  replaced_by_code?: string
}

export interface ProposalEvidence {
  observation_ids: number[]
  invalid_observations: InvalidObservation[]
  evidence_valid: boolean
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
  evidence?: ProposalEvidence
}
