package handler

import (
	"net/http"

	"cadastral-boundary-topology-resolution/backend/internal/dto"
	"github.com/gin-gonic/gin"
)

func (h *CadastralHandler) ListProposals(c *gin.Context) {
	items, meta, err := h.service.ListProposals(dto.ProposalQuery{ParcelID: queryUint(c, "parcel_id"), State: c.Query("state"), Page: queryInt(c, "page", 1), PageSize: queryInt(c, "page_size", 50)})
	if err != nil {
		fail(c, err)
		return
	}
	ok(c, http.StatusOK, items, meta)
}

func (h *CadastralHandler) CreateProposal(c *gin.Context) {
	var req dto.CreateProposalRequest
	if !bind(c, h.validate, &req) {
		return
	}
	item, err := h.service.CreateProposal(req, actor(c))
	if err != nil {
		fail(c, err)
		return
	}
	view, err := h.service.BuildProposalView(item)
	if err != nil {
		fail(c, err)
		return
	}
	ok(c, http.StatusCreated, view, nil)
}

func (h *CadastralHandler) GetProposal(c *gin.Context) {
	id, valid := idParam(c)
	if !valid {
		return
	}
	view, err := h.service.GetProposal(id)
	if err != nil {
		fail(c, err)
		return
	}
	ok(c, http.StatusOK, view, nil)
}

func (h *CadastralHandler) TransitionProposal(c *gin.Context) {
	id, valid := idParam(c)
	if !valid {
		return
	}
	var req dto.ProposalTransitionRequest
	if !bind(c, h.validate, &req) {
		return
	}
	item, err := h.service.TransitionProposal(id, req, actor(c))
	if err != nil {
		fail(c, err)
		return
	}
	view, err := h.service.BuildProposalView(item)
	if err != nil {
		fail(c, err)
		return
	}
	ok(c, http.StatusOK, view, nil)
}

func (h *CadastralHandler) UpdateProposalEvidence(c *gin.Context) {
	id, valid := idParam(c)
	if !valid {
		return
	}
	var req dto.UpdateProposalEvidenceRequest
	if !bind(c, h.validate, &req) {
		return
	}
	item, err := h.service.UpdateProposalEvidence(id, req, actor(c))
	if err != nil {
		fail(c, err)
		return
	}
	view, err := h.service.BuildProposalView(item)
	if err != nil {
		fail(c, err)
		return
	}
	ok(c, http.StatusOK, view, nil)
}
