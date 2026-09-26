package router

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"cadastral-boundary-topology-resolution/backend/internal/constants"
	"cadastral-boundary-topology-resolution/backend/internal/dto"
	"cadastral-boundary-topology-resolution/backend/internal/service"
)

type proposalEvidenceResponse struct {
	Data struct {
		ID            uint   `json:"id"`
		Version       uint   `json:"version"`
		ProposalState string `json:"proposal_state"`
		Evidence      struct {
			ObservationIDs      []uint `json:"observation_ids"`
			EvidenceValid       bool   `json:"evidence_valid"`
			InvalidObservations []struct {
				ObservationID uint   `json:"observation_id"`
				State         string `json:"state"`
				Reason        string `json:"reason"`
			} `json:"invalid_observations"`
		} `json:"evidence"`
	} `json:"data"`
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func proposalEvidenceToken(t *testing.T, engine http.Handler, method, path, token string, body any) (*httptest.ResponseRecorder, proposalEvidenceResponse) {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		encoded, _ := json.Marshal(body)
		reader = bytes.NewReader(encoded)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res := httptest.NewRecorder()
	engine.ServeHTTP(res, req)
	var parsed proposalEvidenceResponse
	_ = json.Unmarshal(res.Body.Bytes(), &parsed)
	return res, parsed
}

func TestProposalEvidenceEndpointBlocksVoidedEvidenceAndListsReasons(t *testing.T) {
	engine, cadastralService, authService := newAuditTestRouter(t)
	surveyorLogin, err := authService.Login(dto.LoginRequest{Username: "surveyor", Password: "DemoPass123!"})
	if err != nil {
		t.Fatalf("login surveyor: %v", err)
	}
	reviewerLogin, err := authService.Login(dto.LoginRequest{Username: "reviewer", Password: "DemoPass123!"})
	if err != nil {
		t.Fatalf("login reviewer: %v", err)
	}
	author := service.Actor{ID: surveyorLogin.User.ID, Username: "surveyor", Role: constants.RoleSurveyor, RequestID: "proposal-evidence"}
	parcel, err := cadastralService.CreateParcel(dto.CreateParcelRequest{
		ParcelCode: "P-PROPOSAL-EVIDENCE", Name: "proposal evidence fixture",
		BoundaryGeoJSON:  `{"type":"Polygon","coordinates":[[[0,0],[10,0],[10,10],[0,10],[0,0]]]}`,
		CoordinateSystem: "EPSG:3857",
	}, author)
	if err != nil {
		t.Fatalf("create parcel: %v", err)
	}
	obs, err := cadastralService.ImportObservation(dto.ImportObservationRequest{
		ParcelID: parcel.ID, ObservationCode: "OBS-PROPOSAL-EVIDENCE", PointGeoJSON: `{"type":"Point","coordinates":[1,1]}`,
		ObservedAt: time.Date(2026, 8, 22, 9, 0, 0, 0, time.UTC), Method: "total_station", HorizontalAccuracyM: 0.02, SourceChecksum: "checksum-proposal-evidence",
	}, author)
	if err != nil {
		t.Fatalf("import observation: %v", err)
	}
	proposal, err := cadastralService.CreateProposal(dto.CreateProposalRequest{
		ParcelID: parcel.ID, BaseVersion: parcel.BoundaryVersion, ProposedGeoJSON: `{"type":"Polygon","coordinates":[[[0,0],[10,0],[10,10],[0,10],[0,0]]]}`,
		ObservationIDs: []uint{obs.ID}, SnapToleranceM: 0.1, Rationale: "evidence endpoint fixture",
	}, author)
	if err != nil {
		t.Fatalf("create proposal: %v", err)
	}
	if _, err := cadastralService.TransitionProposal(proposal.ID, dto.ProposalTransitionRequest{To: string(constants.ProposalValidated), Version: proposal.Version}, author); err != nil {
		t.Fatalf("validate proposal: %v", err)
	}
	if _, err := cadastralService.TransitionObservation(obs.ID, dto.ObservationTransitionRequest{To: "rejected", Version: obs.Version, QualityNote: "fails checksum"}, author); err != nil {
		t.Fatalf("reject observation: %v", err)
	}

	proposalPath := "/api/v1/proposals/" + strconv.FormatUint(uint64(proposal.ID), 10)

	// GET detail surfaces the voided observation id and reason.
	res, detail := proposalEvidenceToken(t, engine, http.MethodGet, proposalPath, surveyorLogin.Token, nil)
	if res.Code != http.StatusOK || detail.Data.Evidence.EvidenceValid {
		t.Fatalf("GET proposal status=%d body=%s evidence=%#v", res.Code, res.Body.String(), detail.Data.Evidence)
	}
	if len(detail.Data.Evidence.InvalidObservations) != 1 || detail.Data.Evidence.InvalidObservations[0].ObservationID != obs.ID {
		t.Fatalf("invalid observations = %#v, want observation %d", detail.Data.Evidence.InvalidObservations, obs.ID)
	}

	// Submission is blocked with 409.
	res, blocked := proposalEvidenceToken(t, engine, http.MethodPost, proposalPath+"/transition", surveyorLogin.Token, map[string]any{"to": "submitted", "version": 2})
	if res.Code != http.StatusConflict || blocked.Error.Code != service.CodeConflict {
		t.Fatalf("blocked submission status=%d body=%s", res.Code, res.Body.String())
	}

	// Re-supporting with the now-rejected observation is a 409 and keeps the
	// proposal on its previous version/state.
	res, repair := proposalEvidenceToken(t, engine, http.MethodPatch, proposalPath+"/evidence", surveyorLogin.Token, map[string]any{"version": 2, "observation_ids": []uint{obs.ID}})
	if res.Code != http.StatusConflict || repair.Error.Code != service.CodeConflict {
		t.Fatalf("repair with voided observation status=%d body=%s", res.Code, res.Body.String())
	}
	res, unchanged := proposalEvidenceToken(t, engine, http.MethodGet, proposalPath, reviewerLogin.Token, nil)
	if res.Code != http.StatusOK || unchanged.Data.Version != 2 || unchanged.Data.ProposalState != string(constants.ProposalValidated) {
		t.Fatalf("proposal changed after failed repair: %s", res.Body.String())
	}

	// A foreign-parcel observation id returns 400 and leaves state untouched.
	other, err := cadastralService.CreateParcel(dto.CreateParcelRequest{
		ParcelCode: "P-PROPOSAL-EVIDENCE-OTHER", Name: "other parcel",
		BoundaryGeoJSON:  `{"type":"Polygon","coordinates":[[[20,0],[30,0],[30,10],[20,10],[20,0]]]}`,
		CoordinateSystem: "EPSG:3857",
	}, author)
	if err != nil {
		t.Fatalf("create other parcel: %v", err)
	}
	foreign, err := cadastralService.ImportObservation(dto.ImportObservationRequest{
		ParcelID: other.ID, ObservationCode: "OBS-FOREIGN-EVIDENCE", PointGeoJSON: `{"type":"Point","coordinates":[21,1]}`,
		ObservedAt: time.Date(2026, 8, 22, 9, 0, 0, 0, time.UTC), Method: "GNSS", HorizontalAccuracyM: 0.03, SourceChecksum: "checksum-foreign-evidence",
	}, author)
	if err != nil {
		t.Fatalf("import foreign observation: %v", err)
	}
	res, foreignRepair := proposalEvidenceToken(t, engine, http.MethodPatch, proposalPath+"/evidence", surveyorLogin.Token, map[string]any{"version": 2, "observation_ids": []uint{foreign.ID}})
	if res.Code != http.StatusBadRequest {
		t.Fatalf("foreign parcel repair status=%d body=%s code=%s", res.Code, res.Body.String(), foreignRepair.Error.Code)
	}
}
