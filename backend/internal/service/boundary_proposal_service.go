package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"cadastral-boundary-topology-resolution/backend/internal/constants"
	"cadastral-boundary-topology-resolution/backend/internal/dto"
	"cadastral-boundary-topology-resolution/backend/internal/geometry"
	"cadastral-boundary-topology-resolution/backend/internal/model"
	"cadastral-boundary-topology-resolution/backend/internal/repository"
)

func (s *CadastralService) CreateProposal(req dto.CreateProposalRequest, actor Actor) (model.BoundaryProposal, error) {
	parcel, err := s.store.Parcels.Get(req.ParcelID)
	if errors.Is(err, repository.ErrNotFound) {
		return model.BoundaryProposal{}, notFound("parcel")
	}
	if err != nil {
		return model.BoundaryProposal{}, internal("load parcel failed", err)
	}
	if req.BaseVersion != parcel.BoundaryVersion {
		return model.BoundaryProposal{}, conflict("proposal base version does not match the parcel boundary", nil)
	}
	proposed, parseErr := geometry.ParsePolygon(req.ProposedGeoJSON)
	if parseErr != nil {
		return model.BoundaryProposal{}, geoInvalid(parseErr)
	}
	if err := s.requireUsableObservations(req.ParcelID, req.ObservationIDs); err != nil {
		return model.BoundaryProposal{}, err
	}
	obsJSON, err := json.Marshal(req.ObservationIDs)
	if err != nil {
		return model.BoundaryProposal{}, internal("encode proposal observations failed", err)
	}
	item := model.BoundaryProposal{
		ParcelID: req.ParcelID, BaseVersion: req.BaseVersion, ProposedGeoJSON: req.ProposedGeoJSON, ObservationIDs: string(obsJSON),
		SnapToleranceM: req.SnapToleranceM, AreaDeltaSquareM: proposed.Area - parcel.AreaSquareM, ProposalState: constants.ProposalDraft,
		Rationale: strings.TrimSpace(req.Rationale), Version: 1, CreatedBy: actor.ID,
	}
	err = s.store.Transaction(func(tx *repository.Store) error {
		if createErr := tx.Proposals.Create(&item); createErr != nil {
			return createErr
		}
		return tx.Audits.Create(audit(actor, "proposal.created", "BoundaryProposal", item.ID, &item.ParcelID, "{}", snapshot(item)))
	})
	if err != nil {
		return item, wrapCadastral(err, "create proposal failed")
	}
	return item, nil
}

// requireUsableObservations verifies a candidate evidence list references only
// existing, accepted observations of the named parcel. Wrong parcel or stale
// evidence is rejected up front so the proposal state never changes.
func (s *CadastralService) requireUsableObservations(parcelID uint, observationIDs []uint) error {
	if len(observationIDs) == 0 {
		return nil
	}
	observations, err := s.store.Observations.ListByIDs(observationIDs)
	if err != nil {
		return internal("load observations failed", err)
	}
	byID := make(map[uint]model.SurveyObservation, len(observations))
	for _, observation := range observations {
		byID[observation.ID] = observation
	}
	seen := map[uint]struct{}{}
	for _, id := range observationIDs {
		if _, duplicated := seen[id]; duplicated {
			continue
		}
		seen[id] = struct{}{}
		observation, found := byID[id]
		if !found {
			return invalid(fmt.Sprintf("observation %d does not exist", id), nil)
		}
		if observation.ParcelID != parcelID {
			return invalid("all observations must belong to the selected parcel", nil)
		}
		if state := constants.ObservationState(observation.ObservationState); !state.UsableAsEvidence() {
			return conflict(fmt.Sprintf("observation %s is %s and can no longer support a proposal", observation.ObservationCode, observation.ObservationState), nil)
		}
	}
	return nil
}

func (s *CadastralService) ListProposals(q dto.ProposalQuery) ([]dto.ProposalView, dto.Pagination, error) {
	normalizePage(&q.Page, &q.PageSize)
	items, total, err := s.store.Proposals.List(q)
	if err != nil {
		return nil, dto.Pagination{}, internal("list proposals failed", err)
	}
	views, err := s.buildProposalViews(items)
	if err != nil {
		return nil, dto.Pagination{}, err
	}
	return views, dto.Pagination{Page: q.Page, PageSize: q.PageSize, Total: total}, nil
}

func (s *CadastralService) GetProposal(id uint) (dto.ProposalView, error) {
	item, err := s.store.Proposals.Get(id)
	if errors.Is(err, repository.ErrNotFound) {
		return dto.ProposalView{}, notFound("proposal")
	}
	if err != nil {
		return dto.ProposalView{}, internal("get proposal failed", err)
	}
	return s.buildProposalView(item)
}

func (s *CadastralService) TransitionProposal(id uint, req dto.ProposalTransitionRequest, actor Actor) (dto.ProposalView, error) {
	item, err := s.store.Proposals.Get(id)
	if errors.Is(err, repository.ErrNotFound) {
		return dto.ProposalView{}, notFound("proposal")
	}
	if err != nil {
		return dto.ProposalView{}, internal("get proposal failed", err)
	}
	to := constants.ProposalState(req.To)
	if !to.Valid() || !constants.CanProposalTransition(item.ProposalState, to) {
		return dto.ProposalView{}, conflict("proposal state transition is not allowed", nil)
	}
	if err := authorizeProposalTransition(item, to, actor); err != nil {
		return dto.ProposalView{}, err
	}
	// Submit and acceptance are the gates that rely on live evidence. A single
	// rejected or superseded reference blocks the move; the proposal keeps its
	// state and version until the author replaces the evidence.
	if to == constants.ProposalSubmitted || to == constants.ProposalAccepted {
		invalid, evidenceErr := s.invalidEvidence(item)
		if evidenceErr != nil {
			return dto.ProposalView{}, evidenceErr
		}
		if len(invalid) > 0 {
			return dto.ProposalView{}, blockedByEvidenceError(invalid)
		}
	}
	updates := map[string]any{}
	if req.Rationale != "" {
		updates["rationale"] = req.Rationale
	}
	if isProposalReviewTransition(to) {
		updates["reviewed_by"] = actor.ID
	}
	err = s.store.Transaction(func(tx *repository.Store) error {
		if transitionErr := tx.Proposals.Transition(id, item.Version, item.ProposalState, to, updates); transitionErr != nil {
			return transitionErr
		}
		return tx.Audits.Create(audit(actor, "proposal.state_changed", "BoundaryProposal", id, &item.ParcelID, snapshot(item), snapshot(map[string]any{"state": to, "version": item.Version + 1})))
	})
	if err != nil {
		return dto.ProposalView{}, conflict("proposal changed while transitioning", err)
	}
	item.ProposalState = to
	item.Version++
	if isProposalReviewTransition(to) {
		item.ReviewedBy = &actor.ID
	}
	return s.buildProposalView(item)
}

// UpdateProposalEvidence swaps stale references for observations that are
// still valid on the same parcel. It is only available while the proposal is
// on the authoring side of the flow; the proposal state itself is unchanged.
func (s *CadastralService) UpdateProposalEvidence(id uint, req dto.ProposalEvidenceUpdateRequest, actor Actor) (dto.ProposalView, error) {
	item, err := s.store.Proposals.Get(id)
	if errors.Is(err, repository.ErrNotFound) {
		return dto.ProposalView{}, notFound("proposal")
	}
	if err != nil {
		return dto.ProposalView{}, internal("get proposal failed", err)
	}
	if item.Version != req.Version {
		return dto.ProposalView{}, conflict("proposal version does not match the current record", nil)
	}
	if !canReplaceEvidence(item.ProposalState) {
		return dto.ProposalView{}, conflict("proposal evidence can only be replaced while the proposal is in draft, validated, or revision state", nil)
	}
	if actor.ID != item.CreatedBy && actor.Role != constants.RoleAdmin {
		return dto.ProposalView{}, &AppError{CodeForbidden, http.StatusForbidden, "only the proposal author or an administrator may replace its evidence", nil}
	}
	if err := s.requireUsableObservations(item.ParcelID, req.ObservationIDs); err != nil {
		return dto.ProposalView{}, err
	}
	beforeIDs, parseErr := parseProposalObservationIDs(item.ObservationIDs)
	if parseErr != nil {
		return dto.ProposalView{}, internal("proposal observation references are not valid JSON", parseErr)
	}
	encoded, encodeErr := encodeObservationIDs(req.ObservationIDs)
	if encodeErr != nil {
		return dto.ProposalView{}, internal("encode proposal observations failed", encodeErr)
	}
	err = s.store.Transaction(func(tx *repository.Store) error {
		if updateErr := tx.Proposals.ReplaceEvidence(id, item.Version, encoded); updateErr != nil {
			return updateErr
		}
		return tx.Audits.Create(audit(actor, "proposal.evidence_replaced", "BoundaryProposal", id, &item.ParcelID,
			snapshot(map[string]any{"observation_ids": beforeIDs, "version": item.Version}),
			snapshot(map[string]any{"observation_ids": req.ObservationIDs, "version": item.Version + 1})))
	})
	if err != nil {
		return dto.ProposalView{}, conflict("proposal changed while replacing evidence", err)
	}
	item.ObservationIDs = encoded
	item.Version++
	return s.buildProposalView(item)
}

func canReplaceEvidence(state constants.ProposalState) bool {
	return state == constants.ProposalDraft || state == constants.ProposalValidated || state == constants.ProposalRevision
}

func authorizeProposalTransition(item model.BoundaryProposal, to constants.ProposalState, actor Actor) error {
	isAuthor := actor.ID == item.CreatedBy
	switch to {
	case constants.ProposalValidated, constants.ProposalSubmitted, constants.ProposalDraft:
		if isAuthor || actor.Role == constants.RoleAdmin {
			return nil
		}
		return &AppError{CodeForbidden, http.StatusForbidden, "only the proposal author or an administrator may advance this authoring transition", nil}
	case constants.ProposalReviewed, constants.ProposalAccepted, constants.ProposalRejected, constants.ProposalRevision:
		if actor.Role != constants.RoleReviewer && actor.Role != constants.RoleAdmin {
			return &AppError{CodeForbidden, http.StatusForbidden, "only a reviewer or administrator may perform review transitions", nil}
		}
		if isAuthor {
			return &AppError{CodeForbidden, http.StatusForbidden, "proposal author cannot review their own proposal", nil}
		}
		return nil
	default:
		return conflict("proposal state transition is not allowed", nil)
	}
}

func isProposalReviewTransition(to constants.ProposalState) bool {
	return to == constants.ProposalReviewed || to == constants.ProposalAccepted || to == constants.ProposalRejected || to == constants.ProposalRevision
}
