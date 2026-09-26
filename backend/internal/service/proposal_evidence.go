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

// parseProposalObservationIDs decodes the archived JSON id list on a proposal.
// Empty evidence is valid; malformed archived data surfaces as an internal
// error rather than silently dropping referenced observations.
func parseProposalObservationIDs(raw string) ([]uint, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return []uint{}, nil
	}
	var ids []uint
	if err := json.Unmarshal([]byte(trimmed), &ids); err != nil {
		return nil, err
	}
	return ids, nil
}

func encodeObservationIDs(ids []uint) (string, error) {
	if len(ids) == 0 {
		return "[]", nil
	}
	encoded, err := json.Marshal(ids)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

// invalidEvidence inspects every observation referenced by a proposal against
// its current record. Rejected and superseded observations, observations that
// ended up on another parcel, and references that no longer resolve are all
// reported; the archived observation numbers and replacement links stay
// visible to reviewers.
func (s *CadastralService) invalidEvidence(proposal model.BoundaryProposal) ([]dto.InvalidEvidence, error) {
	ids, err := parseProposalObservationIDs(proposal.ObservationIDs)
	if err != nil {
		return nil, internal("proposal observation references are not valid JSON", err)
	}
	if len(ids) == 0 {
		return []dto.InvalidEvidence{}, nil
	}
	observations, err := s.store.Observations.ListByIDs(ids)
	if err != nil {
		return nil, internal("load proposal observations failed", err)
	}
	byID := make(map[uint]model.SurveyObservation, len(observations))
	for _, observation := range observations {
		byID[observation.ID] = observation
	}
	// Resolve replacement codes in a single batch for archived supersede links.
	replacementIDs := map[uint]struct{}{}
	for _, observation := range observations {
		if observation.ReplacedBy != nil {
			replacementIDs[*observation.ReplacedBy] = struct{}{}
		}
	}
	replacementIDsList := make([]uint, 0, len(replacementIDs))
	for id := range replacementIDs {
		replacementIDsList = append(replacementIDsList, id)
	}
	sort.Slice(replacementIDsList, func(i, j int) bool { return replacementIDsList[i] < replacementIDsList[j] })
	replacements, err := s.store.Observations.ListByIDs(replacementIDsList)
	if err != nil {
		return nil, internal("load replacement observations failed", err)
	}
	replacementByID := make(map[uint]model.SurveyObservation, len(replacements))
	for _, replacement := range replacements {
		replacementByID[replacement.ID] = replacement
	}

	invalid := make([]dto.InvalidEvidence, 0)
	seen := map[uint]struct{}{}
	for _, id := range ids {
		if _, duplicated := seen[id]; duplicated {
			continue
		}
		seen[id] = struct{}{}
		observation, found := byID[id]
		if !found {
			invalid = append(invalid, dto.InvalidEvidence{
				ObservationID: id,
				Reason:        "referenced observation no longer exists",
			})
			continue
		}
		if observation.ParcelID != proposal.ParcelID {
			invalid = append(invalid, dto.InvalidEvidence{
				ObservationID:   observation.ID,
				ObservationCode: observation.ObservationCode,
				Reason:          "observation belongs to a different parcel than the proposal",
				ParcelID:        observation.ParcelID,
				State:           observation.ObservationState,
			})
			continue
		}
		switch state := constants.ObservationState(observation.ObservationState); state {
		case constants.ObservationAccepted:
			continue
		case constants.ObservationRejected:
			invalid = append(invalid, dto.InvalidEvidence{
				ObservationID:   observation.ID,
				ObservationCode: observation.ObservationCode,
				Reason:          "observation was rejected by review",
				ParcelID:        observation.ParcelID,
				State:           observation.ObservationState,
				QualityNote:     observation.QualityNote,
			})
		case constants.ObservationSuperseded:
			entry := dto.InvalidEvidence{
				ObservationID:   observation.ID,
				ObservationCode: observation.ObservationCode,
				Reason:          "observation was superseded by a newer observation",
				ParcelID:        observation.ParcelID,
				State:           observation.ObservationState,
				ReplacedByID:    observation.ReplacedBy,
				QualityNote:     observation.QualityNote,
			}
			if observation.ReplacedBy != nil {
				if replacement, ok := replacementByID[*observation.ReplacedBy]; ok {
					entry.ReplacedByCode = replacement.ObservationCode
				}
			}
			invalid = append(invalid, entry)
		default:
			invalid = append(invalid, dto.InvalidEvidence{
				ObservationID:   observation.ID,
				ObservationCode: observation.ObservationCode,
				Reason:          fmt.Sprintf("observation state %q is not valid evidence", observation.ObservationState),
				ParcelID:        observation.ParcelID,
				State:           observation.ObservationState,
			})
		}
	}
	return invalid, nil
}

// blockedByEvidenceError builds the 409 shown when a submit/accept transition
// would advance a proposal whose evidence has gone stale.
func blockedByEvidenceError(invalid []dto.InvalidEvidence) error {
	parts := make([]string, 0, len(invalid))
	for _, entry := range invalid {
		code := entry.ObservationCode
		if code == "" {
			code = fmt.Sprintf("#%d", entry.ObservationID)
		}
		parts = append(parts, code+" ("+entry.Reason+")")
	}
	return conflict("proposal cannot advance because referenced evidence is no longer valid: "+strings.Join(parts, "; "), nil)
}

func (s *CadastralService) buildProposalView(proposal model.BoundaryProposal) (dto.ProposalView, error) {
	invalid, err := s.invalidEvidence(proposal)
	if err != nil {
		return dto.ProposalView{}, err
	}
	return dto.ProposalView{BoundaryProposal: proposal, InvalidEvidence: invalid}, nil
}

func (s *CadastralService) buildProposalViews(proposals []model.BoundaryProposal) ([]dto.ProposalView, error) {
	views := make([]dto.ProposalView, 0, len(proposals))
	for _, proposal := range proposals {
		view, err := s.buildProposalView(proposal)
		if err != nil {
			return nil, err
		}
		views = append(views, view)
	}
	return views, nil
}
