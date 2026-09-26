package dto

import "cadastral-boundary-topology-resolution/backend/internal/model"

// ProposalView pairs the persisted proposal with live evidence health so the
// proposal page can show which archived references are currently void.
type ProposalView struct {
	model.BoundaryProposal
	Evidence ProposalEvidence `json:"evidence"`
}

type ProposalQuery struct {
	ParcelID *uint
	State    string
	Page     int
	PageSize int
}

type CreateProposalRequest struct {
	ParcelID        uint    `json:"parcel_id" validate:"required,gt=0"`
	BaseVersion     uint    `json:"base_version" validate:"required,gt=0"`
	ProposedGeoJSON string  `json:"proposed_geojson" validate:"required"`
	ObservationIDs  []uint  `json:"observation_ids" validate:"max=200"`
	SnapToleranceM  float64 `json:"snap_tolerance_m" validate:"required,gt=0,lte=1000"`
	Rationale       string  `json:"rationale" validate:"max=2000"`
}

type ProposalTransitionRequest struct {
	To        string `json:"to" validate:"required"`
	Version   uint   `json:"version" validate:"required,gt=0"`
	Rationale string `json:"rationale" validate:"max=2000"`
}

// UpdateProposalEvidenceRequest lets an author re-support a draft proposal
// with observations that are still valid on the same parcel.
type UpdateProposalEvidenceRequest struct {
	Version        uint   `json:"version" validate:"required,gt=0"`
	ObservationIDs []uint `json:"observation_ids" validate:"max=200"`
}

// InvalidObservation explains why one referenced observation no longer
// supports a proposal. Voided references stay archived in observation_ids.
type InvalidObservation struct {
	ObservationID   uint   `json:"observation_id"`
	ObservationCode string `json:"observation_code"`
	State           string `json:"state"`
	Reason          string `json:"reason"`
	ReplacedByID    *uint  `json:"replaced_by_id,omitempty"`
	ReplacedByCode  string `json:"replaced_by_code,omitempty"`
}

// ProposalEvidence describes the current evidence health of a proposal.
type ProposalEvidence struct {
	ObservationIDs      []uint               `json:"observation_ids"`
	InvalidObservations []InvalidObservation `json:"invalid_observations"`
	EvidenceValid       bool                 `json:"evidence_valid"`
}
