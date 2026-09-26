package dto

import "cadastral-boundary-topology-resolution/backend/internal/model"

// ProposalEvidenceUpdateRequest replaces the supporting observations of a
// proposal that is still on the authoring side of the review flow.
type ProposalEvidenceUpdateRequest struct {
	Version        uint   `json:"version" validate:"required,gt=0"`
	ObservationIDs []uint `json:"observation_ids" validate:"max=200"`
}

// InvalidEvidence explains why one observation referenced by a proposal can no
// longer support it. Missing references are kept here as well so an audit trail
// never silently hides evidence that disappeared.
type InvalidEvidence struct {
	ObservationID   uint   `json:"observation_id"`
	ObservationCode string `json:"observation_code,omitempty"`
	Reason          string `json:"reason"`
	ParcelID        uint   `json:"parcel_id,omitempty"`
	State           string `json:"state,omitempty"`
	ReplacedByID    *uint  `json:"replaced_by_id,omitempty"`
	ReplacedByCode  string `json:"replaced_by_code,omitempty"`
	QualityNote     string `json:"quality_note,omitempty"`
}

// ProposalView is the proposal contract served over HTTP. The proposal embeds
// the persisted record while InvalidEvidence is projected from the current
// observation states; archived codes and replacement links remain visible.
type ProposalView struct {
	model.BoundaryProposal
	InvalidEvidence []InvalidEvidence `json:"invalid_evidence"`
}
