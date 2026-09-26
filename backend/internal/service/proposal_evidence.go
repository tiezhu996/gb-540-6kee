package service

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"cadastral-boundary-topology-resolution/backend/internal/constants"
	"cadastral-boundary-topology-resolution/backend/internal/dto"
	"cadastral-boundary-topology-resolution/backend/internal/model"
)

// proposalObservationIDs decodes the archived JSON array of evidence ids. The
// raw string is retained on the proposal so voided references stay traceable.
func proposalObservationIDs(raw string) []uint {
	var ids []uint
	if err := json.Unmarshal([]byte(raw), &ids); err != nil {
		return nil
	}
	return ids
}

// BuildProposalViews attaches the current evidence health of each proposal.
// Referenced observations and their replacements are loaded in bulk so list
// pages never issue one query per proposal.
func (s *CadastralService) BuildProposalViews(items []model.BoundaryProposal) ([]dto.ProposalView, error) {
	idSet := map[uint]bool{}
	for _, item := range items {
		for _, id := range proposalObservationIDs(item.ObservationIDs) {
			idSet[id] = true
		}
	}
	loaded, err := s.store.Observations.ListByIDs(keysUint(idSet))
	if err != nil {
		return nil, internal("load proposal evidence failed", err)
	}
	replacements, err := s.replacementIndex(loaded)
	if err != nil {
		return nil, err
	}
	views := make([]dto.ProposalView, 0, len(items))
	for _, item := range items {
		evidence := evaluateProposalEvidence(item, loaded, replacements)
		views = append(views, dto.ProposalView{BoundaryProposal: item, Evidence: evidence})
	}
	return views, nil
}

func (s *CadastralService) replacementIndex(observations map[uint]model.SurveyObservation) (map[uint]model.SurveyObservation, error) {
	idSet := map[uint]bool{}
	for _, obs := range observations {
		if obs.ReplacedBy != nil {
			idSet[*obs.ReplacedBy] = true
		}
	}
	loaded, err := s.store.Observations.ListByIDs(keysUint(idSet))
	if err != nil {
		return nil, internal("load replacement observations failed", err)
	}
	return loaded, nil
}

func keysUint(set map[uint]bool) []uint {
	ids := make([]uint, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	return ids
}

// BuildProposalView attaches live evidence health to one proposal.
func (s *CadastralService) BuildProposalView(item model.BoundaryProposal) (dto.ProposalView, error) {
	views, err := s.BuildProposalViews([]model.BoundaryProposal{item})
	if err != nil {
		return dto.ProposalView{}, err
	}
	return views[0], nil
}

// evaluateProposalEvidence flags every referenced observation that is no
// longer accepted evidence for the proposal's parcel. Missing, rejected,
// superseded, and foreign-parcel observations are all reported individually.
func evaluateProposalEvidence(proposal model.BoundaryProposal, observations, replacements map[uint]model.SurveyObservation) dto.ProposalEvidence {
	ids := proposalObservationIDs(proposal.ObservationIDs)
	evidence := dto.ProposalEvidence{ObservationIDs: ids, InvalidObservations: []dto.InvalidObservation{}, EvidenceValid: true}
	sorted := append([]uint(nil), ids...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	for _, id := range sorted {
		obs, ok := observations[id]
		switch {
		case !ok:
			evidence.EvidenceValid = false
			evidence.InvalidObservations = append(evidence.InvalidObservations, dto.InvalidObservation{
				ObservationID: id, ObservationCode: fmt.Sprintf("#%d", id), State: "missing",
				Reason: "the observation record no longer exists",
			})
		case obs.ParcelID != proposal.ParcelID:
			evidence.EvidenceValid = false
			evidence.InvalidObservations = append(evidence.InvalidObservations, dto.InvalidObservation{
				ObservationID: obs.ID, ObservationCode: obs.ObservationCode, State: "foreign_parcel",
				Reason: fmt.Sprintf("the observation belongs to parcel %d, not parcel %d", obs.ParcelID, proposal.ParcelID),
			})
		case obs.ObservationState != constants.ObservationAccepted:
			evidence.EvidenceValid = false
			evidence.InvalidObservations = append(evidence.InvalidObservations, invalidObservationView(obs, replacements))
		}
	}
	return evidence
}

func invalidObservationView(obs model.SurveyObservation, replacements map[uint]model.SurveyObservation) dto.InvalidObservation {
	invalid := dto.InvalidObservation{ObservationID: obs.ID, ObservationCode: obs.ObservationCode, State: obs.ObservationState, ReplacedByID: obs.ReplacedBy}
	switch obs.ObservationState {
	case constants.ObservationRejected:
		invalid.Reason = "the observation was rejected by a reviewer"
		if note := obs.QualityNote; note != "" {
			invalid.Reason += ": " + note
		}
	case constants.ObservationSuperseded:
		if obs.ReplacedBy != nil {
			if replacement, ok := replacements[*obs.ReplacedBy]; ok && replacement.ObservationCode != "" {
				invalid.ReplacedByCode = replacement.ObservationCode
				invalid.Reason = fmt.Sprintf("the observation was superseded by observation %s (#%d) on the same parcel", replacement.ObservationCode, *obs.ReplacedBy)
				break
			}
			invalid.Reason = fmt.Sprintf("the observation was superseded by observation #%d on the same parcel", *obs.ReplacedBy)
			break
		}
		invalid.Reason = "the observation was superseded by a newer observation on the same parcel"
	default:
		invalid.Reason = "the observation is no longer accepted evidence"
	}
	return invalid
}

// validateEvidenceSelection verifies a new or replacement evidence set before
// any write: ids must be unique, present, on the proposal's parcel, and
// accepted. Rejected and superseded observations are state conflicts, not bad
// input, so callers can distinguish "voided evidence" from malformed requests.
func (s *CadastralService) validateEvidenceSelection(parcelID uint, ids []uint) error {
	loaded, err := s.store.Observations.ListByIDs(ids)
	if err != nil {
		return internal("load observations failed", err)
	}
	replacements, err := s.replacementIndex(loaded)
	if err != nil {
		return err
	}
	seen := make(map[uint]bool, len(ids))
	for _, id := range ids {
		if seen[id] {
			return invalid(fmt.Sprintf("observation #%d is referenced more than once", id), nil)
		}
		seen[id] = true
		obs, ok := loaded[id]
		if !ok {
			return invalid(fmt.Sprintf("observation #%d does not exist", id), nil)
		}
		if obs.ParcelID != parcelID {
			return invalid(fmt.Sprintf("observation %s (#%d) belongs to parcel %d; evidence must come from the selected parcel %d", obs.ObservationCode, id, obs.ParcelID, parcelID), nil)
		}
		switch obs.ObservationState {
		case constants.ObservationAccepted:
			continue
		case constants.ObservationRejected, constants.ObservationSuperseded:
			return conflict("observation "+obs.ObservationCode+" is no longer valid evidence: "+invalidObservationView(obs, replacements).Reason, nil)
		default:
			return conflict("observation "+obs.ObservationCode+" is not accepted evidence", nil)
		}
	}
	return nil
}

// requireActiveProposalEvidence blocks submission and acceptance while any
// referenced observation is void. It reads current observation state, never
// the state captured when the proposal was drafted.
func (s *CadastralService) requireActiveProposalEvidence(proposal model.BoundaryProposal) error {
	ids := proposalObservationIDs(proposal.ObservationIDs)
	loaded, err := s.store.Observations.ListByIDs(ids)
	if err != nil {
		return internal("load proposal evidence failed", err)
	}
	replacements, err := s.replacementIndex(loaded)
	if err != nil {
		return err
	}
	evidence := evaluateProposalEvidence(proposal, loaded, replacements)
	if evidence.EvidenceValid {
		return nil
	}
	codes := make([]string, 0, len(evidence.InvalidObservations))
	for _, invalidObs := range evidence.InvalidObservations {
		codes = append(codes, invalidObs.ObservationCode)
	}
	return conflict(fmt.Sprintf("proposal references %d voided observation(s) (%s); the author must re-support it with accepted observations on the same parcel before submitting or accepting", len(codes), strings.Join(codes, ", ")), nil)
}
