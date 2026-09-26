package service

import (
	"encoding/json"
	"errors"
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
	// A proposal can only be drafted on evidence that is currently accepted on
	// the selected parcel; rejected or superseded observations never enter the
	// archived evidence set.
	if err := s.validateEvidenceSelection(req.ParcelID, req.ObservationIDs); err != nil {
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

func (s *CadastralService) ListProposals(q dto.ProposalQuery) ([]dto.ProposalView, dto.Pagination, error) {
	normalizePage(&q.Page, &q.PageSize)
	items, total, err := s.store.Proposals.List(q)
	if err != nil {
		return nil, dto.Pagination{}, internal("list proposals failed", err)
	}
	views, err := s.BuildProposalViews(items)
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
	return s.BuildProposalView(item)
}

func (s *CadastralService) TransitionProposal(id uint, req dto.ProposalTransitionRequest, actor Actor) (model.BoundaryProposal, error) {
	item, err := s.store.Proposals.Get(id)
	if errors.Is(err, repository.ErrNotFound) {
		return item, notFound("proposal")
	}
	if err != nil {
		return item, internal("get proposal failed", err)
	}
	to := constants.ProposalState(req.To)
	if !to.Valid() || !constants.CanProposalTransition(item.ProposalState, to) {
		return item, conflict("proposal state transition is not allowed", nil)
	}
	if err := authorizeProposalTransition(item, to, actor); err != nil {
		return item, err
	}
	// Submission and acceptance must be blocked while any referenced
	// observation has been rejected, superseded, or otherwise voided.
	if to == constants.ProposalSubmitted || to == constants.ProposalAccepted {
		if err := s.requireActiveProposalEvidence(item); err != nil {
			return item, err
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
		return item, conflict("proposal changed while transitioning", err)
	}
	item.ProposalState = to
	item.Version++
	if isProposalReviewTransition(to) {
		item.ReviewedBy = &actor.ID
	}
	return item, nil
}

// UpdateProposalEvidence replaces the archived evidence set of a draft-stage
// proposal. It is the author's recovery path after referenced observations
// were rejected or superseded: every replacement observation must be accepted
// and belong to the same parcel. Validation runs before any write, so a wrong
// parcel or a voided observation leaves the proposal exactly as it was.
func (s *CadastralService) UpdateProposalEvidence(id uint, req dto.UpdateProposalEvidenceRequest, actor Actor) (model.BoundaryProposal, error) {
	item, err := s.store.Proposals.Get(id)
	if errors.Is(err, repository.ErrNotFound) {
		return item, notFound("proposal")
	}
	if err != nil {
		return item, internal("get proposal failed", err)
	}
	if item.Version != req.Version {
		return item, conflict("proposal version does not match the current record", nil)
	}
	if item.ProposalState != constants.ProposalDraft && item.ProposalState != constants.ProposalValidated && item.ProposalState != constants.ProposalRevision {
		return item, conflict("proposal evidence can only be edited while the proposal is in draft, validated, or revision state", nil)
	}
	if actor.ID != item.CreatedBy && actor.Role != constants.RoleAdmin {
		return item, &AppError{CodeForbidden, http.StatusForbidden, "only the proposal author or an administrator may update proposal evidence", nil}
	}
	if err := s.validateEvidenceSelection(item.ParcelID, req.ObservationIDs); err != nil {
		return item, err
	}
	obsJSON, err := json.Marshal(req.ObservationIDs)
	if err != nil {
		return item, internal("encode proposal observations failed", err)
	}
	// Re-worked evidence invalidates validation: validated and revision-stage
	// proposals return to draft and must pass the full flow again.
	resetState := item.ProposalState != constants.ProposalDraft
	before := item
	err = s.store.Transaction(func(tx *repository.Store) error {
		if updateErr := tx.Proposals.UpdateEvidence(id, item.Version, string(obsJSON), resetState); updateErr != nil {
			return updateErr
		}
		after := map[string]any{"observation_ids": req.ObservationIDs, "version": item.Version + 1}
		if resetState {
			after["proposal_state"] = constants.ProposalDraft
		}
		return tx.Audits.Create(audit(actor, "proposal.evidence_updated", "BoundaryProposal", id, &item.ParcelID, snapshot(before), snapshot(after)))
	})
	if err != nil {
		return item, conflict("proposal changed while updating evidence", err)
	}
	item.ObservationIDs = string(obsJSON)
	item.Version++
	if resetState {
		item.ProposalState = constants.ProposalDraft
	}
	return item, nil
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
