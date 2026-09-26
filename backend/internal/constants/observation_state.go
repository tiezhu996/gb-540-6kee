package constants

// ObservationState describes the review lifecycle of survey evidence. Only
// accepted observations are valid support for a boundary proposal; rejected
// evidence stays rejected evidence and a superseded observation is archived
// together with its replacement link.
type ObservationState string

const (
	ObservationAccepted   ObservationState = "accepted"
	ObservationRejected   ObservationState = "rejected"
	ObservationSuperseded ObservationState = "superseded"
)

func (s ObservationState) Valid() bool {
	return s == ObservationAccepted || s == ObservationRejected || s == ObservationSuperseded
}

// UsableAsEvidence reports whether the observation can currently support a
// boundary proposal.
func (s ObservationState) UsableAsEvidence() bool { return s == ObservationAccepted }
