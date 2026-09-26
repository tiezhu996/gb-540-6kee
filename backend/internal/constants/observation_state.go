package constants

// ObservationState marks survey evidence as usable or void. Only accepted
// observations may support a proposal; rejected evidence failed review and
// superseded evidence was replaced by a newer observation on the same parcel.
const (
	ObservationAccepted   = "accepted"
	ObservationRejected   = "rejected"
	ObservationSuperseded = "superseded"
)

// ObservationStateValid reports whether state is one of the persisted
// observation lifecycle states.
func ObservationStateValid(state string) bool {
	return state == ObservationAccepted || state == ObservationRejected || state == ObservationSuperseded
}
