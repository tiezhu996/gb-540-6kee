package service

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"cadastral-boundary-topology-resolution/backend/internal/constants"
	"cadastral-boundary-topology-resolution/backend/internal/dto"
	"cadastral-boundary-topology-resolution/backend/internal/model"
	"cadastral-boundary-topology-resolution/backend/internal/repository"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func serviceTestPolygon(points string) string {
	return `{"type":"Polygon","coordinates":[[` + points + `]]}`
}

func newCadastralTestService(t *testing.T) (*CadastralService, *repository.Store) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open SQLite: %v", err)
	}
	if err := db.AutoMigrate(&model.User{}, &model.AuditLog{}, &model.LandParcel{}, &model.SurveyObservation{}, &model.BoundaryProposal{}, &model.TopologyConflict{}, &model.TopologyDetectionRun{}); err != nil {
		t.Fatalf("migrate SQLite: %v", err)
	}
	store := repository.NewStore(db)
	return NewCadastralService(store), store
}

func testActor(id uint, role, requestID string) Actor {
	return Actor{ID: id, Username: fmt.Sprintf("user-%d", id), Role: role, RequestID: requestID}
}

func createTestParcel(t *testing.T, svc *CadastralService, code, boundary string, actor Actor) model.LandParcel {
	t.Helper()
	parcel, err := svc.CreateParcel(dto.CreateParcelRequest{
		ParcelCode: code, Name: code, BoundaryGeoJSON: boundary,
		CoordinateSystem: "EPSG:3857", OwnerOrg: "test survey office",
	}, actor)
	if err != nil {
		t.Fatalf("CreateParcel(%s) error = %v", code, err)
	}
	return parcel
}

func createTestProposal(t *testing.T, svc *CadastralService, parcel model.LandParcel, boundary string, actor Actor) model.BoundaryProposal {
	t.Helper()
	proposal, err := svc.CreateProposal(dto.CreateProposalRequest{
		ParcelID: parcel.ID, BaseVersion: parcel.BoundaryVersion, ProposedGeoJSON: boundary,
		SnapToleranceM: 0.1, Rationale: "survey evidence supports the adjusted boundary",
	}, actor)
	if err != nil {
		t.Fatalf("CreateProposal() error = %v", err)
	}
	return proposal
}

func TestDetectConflictsIsIdempotentAndRejectsRequestKeyReuse(t *testing.T) {
	svc, store := newCadastralTestService(t)
	surveyor := testActor(101, constants.RoleSurveyor, "parcel-create")
	base := createTestParcel(t, svc, "P-BASE", serviceTestPolygon(`[0,0],[10,0],[10,10],[0,10],[0,0]`), surveyor)
	createTestParcel(t, svc, "P-NEIGHBOR", serviceTestPolygon(`[10,0],[20,0],[20,10],[10,10],[10,0]`), testActor(102, constants.RoleSurveyor, "neighbour-create"))
	proposal := createTestProposal(t, svc, base, serviceTestPolygon(`[0,0],[11,0],[11,10],[0,10],[0,0]`), testActor(101, constants.RoleSurveyor, "proposal-create"))

	detectionActor := testActor(101, constants.RoleGISAnalyst, "detect-first")
	request := dto.DetectConflictRequest{ProposalID: proposal.ID, SnapToleranceM: 0.1}
	first, err := svc.DetectConflicts(request, "detection-idempotency-key", detectionActor)
	if err != nil {
		t.Fatalf("first DetectConflicts() error = %v", err)
	}
	if len(first) == 0 || first[0].ConflictType != constants.ConflictOverlap {
		t.Fatalf("first DetectConflicts() = %#v, want an overlap", first)
	}
	second, err := svc.DetectConflicts(request, "detection-idempotency-key", testActor(101, constants.RoleGISAnalyst, "detect-replay"))
	if err != nil {
		t.Fatalf("replayed DetectConflicts() error = %v", err)
	}
	if len(second) != len(first) || second[0].ID != first[0].ID {
		t.Fatalf("replayed result = %#v, first result = %#v", second, first)
	}
	var runCount int64
	if err := store.DB.Model(&model.TopologyDetectionRun{}).Count(&runCount).Error; err != nil {
		t.Fatalf("count detection runs: %v", err)
	}
	if runCount != 1 {
		t.Fatalf("detection run count = %d, want 1", runCount)
	}
	_, err = svc.DetectConflicts(dto.DetectConflictRequest{ProposalID: proposal.ID, SnapToleranceM: 0.2}, "detection-idempotency-key", detectionActor)
	var appErr *AppError
	if !errors.As(err, &appErr) || appErr.Code != CodeConflict || appErr.Status != 409 {
		t.Fatalf("changed request with same key error = %v, want 409 %s", err, CodeConflict)
	}
}

func TestProposalReviewRequiresIndependentReviewer(t *testing.T) {
	svc, _ := newCadastralTestService(t)
	author := testActor(201, constants.RoleSurveyor, "author-create")
	parcel := createTestParcel(t, svc, "P-REVIEW", serviceTestPolygon(`[0,0],[10,0],[10,10],[0,10],[0,0]`), author)
	proposal := createTestProposal(t, svc, parcel, serviceTestPolygon(`[0,0],[10,0],[10,10],[0,10],[0,0]`), author)

	validated, err := svc.TransitionProposal(proposal.ID, dto.ProposalTransitionRequest{To: string(constants.ProposalValidated), Version: proposal.Version}, testActor(201, constants.RoleSurveyor, "author-validate"))
	if err != nil {
		t.Fatalf("validate proposal: %v", err)
	}
	_, err = svc.TransitionProposal(proposal.ID, dto.ProposalTransitionRequest{To: string(constants.ProposalSubmitted), Version: validated.Version}, testActor(202, constants.RoleSurveyor, "other-author-submit"))
	var appErr *AppError
	if !errors.As(err, &appErr) || appErr.Code != CodeForbidden || appErr.Status != 403 {
		t.Fatalf("non-author authoring transition error = %v, want 403 %s", err, CodeForbidden)
	}
	submitted, err := svc.TransitionProposal(proposal.ID, dto.ProposalTransitionRequest{To: string(constants.ProposalSubmitted), Version: validated.Version}, testActor(201, constants.RoleSurveyor, "author-submit"))
	if err != nil {
		t.Fatalf("submit proposal: %v", err)
	}
	_, err = svc.TransitionProposal(proposal.ID, dto.ProposalTransitionRequest{To: string(constants.ProposalReviewed), Version: submitted.Version}, testActor(201, constants.RoleReviewer, "self-review"))
	if !errors.As(err, &appErr) || appErr.Code != CodeForbidden || appErr.Status != 403 {
		t.Fatalf("author self-review error = %v, want 403 %s", err, CodeForbidden)
	}
	_, err = svc.TransitionProposal(proposal.ID, dto.ProposalTransitionRequest{To: string(constants.ProposalReviewed), Version: submitted.Version}, testActor(201, constants.RoleAdmin, "admin-self-review"))
	if !errors.As(err, &appErr) || appErr.Code != CodeForbidden || appErr.Status != 403 {
		t.Fatalf("administrator self-review error = %v, want 403 %s", err, CodeForbidden)
	}
	_, err = svc.TransitionProposal(proposal.ID, dto.ProposalTransitionRequest{To: string(constants.ProposalReviewed), Version: submitted.Version}, testActor(202, constants.RoleSurveyor, "unprivileged-review"))
	if !errors.As(err, &appErr) || appErr.Code != CodeForbidden || appErr.Status != 403 {
		t.Fatalf("non-reviewer review error = %v, want 403 %s", err, CodeForbidden)
	}
	reviewed, err := svc.TransitionProposal(proposal.ID, dto.ProposalTransitionRequest{To: string(constants.ProposalReviewed), Version: submitted.Version}, testActor(203, constants.RoleReviewer, "independent-review"))
	if err != nil {
		t.Fatalf("independent reviewer transition: %v", err)
	}
	if reviewed.ProposalState != constants.ProposalReviewed || reviewed.ReviewedBy == nil || *reviewed.ReviewedBy != 203 {
		t.Fatalf("reviewed proposal = %#v, want reviewer 203", reviewed)
	}
	accepted, err := svc.TransitionProposal(proposal.ID, dto.ProposalTransitionRequest{To: string(constants.ProposalAccepted), Version: reviewed.Version}, testActor(203, constants.RoleReviewer, "independent-accept"))
	if err != nil {
		t.Fatalf("independent reviewer acceptance: %v", err)
	}
	if accepted.ProposalState != constants.ProposalAccepted || accepted.Version != reviewed.Version+1 {
		t.Fatalf("accepted proposal = %#v, want accepted state and incremented version", accepted)
	}
}

func TestCreateParcelRejectsSelfIntersectingGeometryWith422(t *testing.T) {
	svc, _ := newCadastralTestService(t)
	_, err := svc.CreateParcel(dto.CreateParcelRequest{
		ParcelCode:       "P-BOWTIE",
		Name:             "self intersecting fixture",
		BoundaryGeoJSON:  serviceTestPolygon(`[0,0],[10,10],[0,10],[10,0],[0,0]`),
		CoordinateSystem: "EPSG:3857",
	}, testActor(401, constants.RoleSurveyor, "invalid-geometry"))
	var appErr *AppError
	if !errors.As(err, &appErr) || appErr.Code != CodeInvalidInput || appErr.Status != 422 {
		t.Fatalf("CreateParcel(self-intersecting) error = %v, want 422 %s", err, CodeInvalidInput)
	}
}

func TestSupersedingObservationRecordsReplacement(t *testing.T) {
	svc, _ := newCadastralTestService(t)
	actor := testActor(501, constants.RoleSurveyor, "observation-import")
	parcel := createTestParcel(t, svc, "P-OBS", serviceTestPolygon(`[0,0],[10,0],[10,10],[0,10],[0,0]`), actor)

	first, err := svc.ImportObservation(dto.ImportObservationRequest{
		ParcelID: parcel.ID, ObservationCode: "OBS-ORIGINAL", PointGeoJSON: `{"type":"Point","coordinates":[1,1]}`,
		ObservedAt: time.Date(2026, 8, 22, 9, 0, 0, 0, time.UTC), Method: "total_station", HorizontalAccuracyM: 0.02, SourceChecksum: "checksum-original",
	}, actor)
	if err != nil {
		t.Fatalf("import original observation: %v", err)
	}
	replacement, err := svc.ImportObservation(dto.ImportObservationRequest{
		ParcelID: parcel.ID, ObservationCode: "OBS-REPLACEMENT", PointGeoJSON: `{"type":"Point","coordinates":[1.02,1]}`,
		ObservedAt: time.Date(2026, 8, 22, 9, 1, 0, 0, time.UTC), Method: "total_station", HorizontalAccuracyM: 0.01, SourceChecksum: "checksum-replacement",
	}, actor)
	if err != nil {
		t.Fatalf("import replacement observation: %v", err)
	}

	superseded, err := svc.TransitionObservation(first.ID, dto.ObservationTransitionRequest{
		To: "superseded", Version: first.Version, ReplacementObservationID: &replacement.ID, QualityNote: "superseded by a more accurate repeat observation",
	}, testActor(501, constants.RoleSurveyor, "observation-supersede"))
	if err != nil {
		t.Fatalf("supersede observation: %v", err)
	}
	if superseded.ObservationState != "superseded" || superseded.ReplacedBy == nil || *superseded.ReplacedBy != replacement.ID {
		t.Fatalf("superseded observation = %#v, want replacement %d", superseded, replacement.ID)
	}
	persisted, err := svc.GetObservation(first.ID)
	if err != nil {
		t.Fatalf("reload superseded observation: %v", err)
	}
	if persisted.Version != first.Version+1 || persisted.ReplacedBy == nil || *persisted.ReplacedBy != replacement.ID {
		t.Fatalf("persisted observation = %#v, want incremented version and replacement", persisted)
	}
}

func importTestObservation(t *testing.T, svc *CadastralService, parcelID uint, code string, actor Actor) model.SurveyObservation {
	t.Helper()
	observation, err := svc.ImportObservation(dto.ImportObservationRequest{
		ParcelID: parcelID, ObservationCode: code, PointGeoJSON: `{"type":"Point","coordinates":[1,1]}`,
		ObservedAt: time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC), Method: "total_station", HorizontalAccuracyM: 0.02, SourceChecksum: "checksum-" + code,
	}, actor)
	if err != nil {
		t.Fatalf("import observation %s: %v", code, err)
	}
	return observation
}

func createProposalWithObservations(t *testing.T, svc *CadastralService, parcel model.LandParcel, observationIDs []uint, actor Actor) model.BoundaryProposal {
	t.Helper()
	proposal, err := svc.CreateProposal(dto.CreateProposalRequest{
		ParcelID: parcel.ID, BaseVersion: parcel.BoundaryVersion,
		ProposedGeoJSON: serviceTestPolygon(`[0,0],[10,0],[10,10],[0,10],[0,0]`),
		ObservationIDs:  observationIDs, SnapToleranceM: 0.1, Rationale: "evidence-backed proposal",
	}, actor)
	if err != nil {
		t.Fatalf("CreateProposal(with observations) error = %v", err)
	}
	return proposal
}

func assertConflictError(t *testing.T, err error, wantCode string) {
	t.Helper()
	var appErr *AppError
	if !errors.As(err, &appErr) || appErr.Code != wantCode || appErr.Status != 409 {
		t.Fatalf("error = %v, want 409 %s", err, wantCode)
	}
}

// A proposal sitting on validated evidence blocks submission once a referenced
// observation is rejected, and the proposal record itself must not change.
func TestProposalSubmitBlockedWhenReferencedObservationRejected(t *testing.T) {
	svc, _ := newCadastralTestService(t)
	author := testActor(601, constants.RoleSurveyor, "author")
	parcel := createTestParcel(t, svc, "P-REJECTED-EVIDENCE", serviceTestPolygon(`[0,0],[10,0],[10,10],[0,10],[0,0]`), author)
	observation := importTestObservation(t, svc, parcel.ID, "OBS-SUBMIT-REJECT", author)
	proposal := createProposalWithObservations(t, svc, parcel, []uint{observation.ID}, author)
	validated, err := svc.TransitionProposal(proposal.ID, dto.ProposalTransitionRequest{To: string(constants.ProposalValidated), Version: proposal.Version}, author)
	if err != nil {
		t.Fatalf("validate proposal: %v", err)
	}

	if _, err := svc.TransitionObservation(observation.ID, dto.ObservationTransitionRequest{To: "rejected", Version: observation.Version, QualityNote: "failed cross-check"}, author); err != nil {
		t.Fatalf("reject observation: %v", err)
	}
	_, err = svc.TransitionProposal(proposal.ID, dto.ProposalTransitionRequest{To: string(constants.ProposalSubmitted), Version: validated.Version}, author)
	assertConflictError(t, err, CodeConflict)

	view, err := svc.GetProposal(proposal.ID)
	if err != nil {
		t.Fatalf("get proposal after blocked submit: %v", err)
	}
	if view.ProposalState != constants.ProposalValidated || view.Version != validated.Version {
		t.Fatalf("proposal = state %s v%d, want unchanged validated v%d", view.ProposalState, view.Version, validated.Version)
	}
	if len(view.InvalidEvidence) != 1 {
		t.Fatalf("invalid evidence = %#v, want one rejected entry", view.InvalidEvidence)
	}
	entry := view.InvalidEvidence[0]
	if entry.ObservationID != observation.ID || entry.ObservationCode != "OBS-SUBMIT-REJECT" || entry.State != "rejected" {
		t.Fatalf("invalid evidence entry = %#v", entry)
	}
}

// Acceptance is the second evidence gate: a reviewer cannot accept a reviewed
// proposal whose observation was superseded; the archived replacement link is
// reported with the invalid code.
func TestProposalAcceptBlockedWhenEvidenceSuperseded(t *testing.T) {
	svc, _ := newCadastralTestService(t)
	author := testActor(611, constants.RoleSurveyor, "author-supersede")
	reviewer := testActor(612, constants.RoleReviewer, "reviewer-supersede")
	parcel := createTestParcel(t, svc, "P-SUPERSEDED-EVIDENCE", serviceTestPolygon(`[0,0],[10,0],[10,10],[0,10],[0,0]`), author)
	original := importTestObservation(t, svc, parcel.ID, "OBS-OLD", author)
	replacement := importTestObservation(t, svc, parcel.ID, "OBS-NEW", author)
	proposal := createProposalWithObservations(t, svc, parcel, []uint{original.ID}, author)
	validated, err := svc.TransitionProposal(proposal.ID, dto.ProposalTransitionRequest{To: string(constants.ProposalValidated), Version: proposal.Version}, author)
	if err != nil {
		t.Fatalf("validate proposal: %v", err)
	}
	submitted, err := svc.TransitionProposal(proposal.ID, dto.ProposalTransitionRequest{To: string(constants.ProposalSubmitted), Version: validated.Version}, author)
	if err != nil {
		t.Fatalf("submit proposal: %v", err)
	}

	if _, err := svc.TransitionObservation(original.ID, dto.ObservationTransitionRequest{To: "superseded", Version: original.Version, ReplacementObservationID: &replacement.ID}, author); err != nil {
		t.Fatalf("supersede observation: %v", err)
	}
	reviewed, err := svc.TransitionProposal(proposal.ID, dto.ProposalTransitionRequest{To: string(constants.ProposalReviewed), Version: submitted.Version}, reviewer)
	if err != nil {
		t.Fatalf("review transition should not be gated by evidence: %v", err)
	}
	_, err = svc.TransitionProposal(proposal.ID, dto.ProposalTransitionRequest{To: string(constants.ProposalAccepted), Version: reviewed.Version}, reviewer)
	assertConflictError(t, err, CodeConflict)

	view, err := svc.GetProposal(proposal.ID)
	if err != nil {
		t.Fatalf("get blocked proposal: %v", err)
	}
	if len(view.InvalidEvidence) != 1 || view.InvalidEvidence[0].State != "superseded" {
		t.Fatalf("invalid evidence = %#v, want one superseded entry", view.InvalidEvidence)
	}
	entry := view.InvalidEvidence[0]
	if entry.ReplacedByID == nil || *entry.ReplacedByID != replacement.ID || entry.ReplacedByCode != "OBS-NEW" {
		t.Fatalf("replacement link = %#v, want archived replacement OBS-NEW", entry)
	}
}

// The author recovers by replacing stale evidence with another still-accepted
// observation on the same parcel, after which the flow continues.
func TestAuthorReplacesInvalidEvidenceThenProposalAdvances(t *testing.T) {
	svc, _ := newCadastralTestService(t)
	author := testActor(621, constants.RoleSurveyor, "author-recovery")
	reviewer := testActor(622, constants.RoleReviewer, "reviewer-recovery")
	parcel := createTestParcel(t, svc, "P-RECOVER", serviceTestPolygon(`[0,0],[10,0],[10,10],[0,10],[0,0]`), author)
	original := importTestObservation(t, svc, parcel.ID, "OBS-RECOVER-OLD", author)
	fresh := importTestObservation(t, svc, parcel.ID, "OBS-RECOVER-NEW", author)
	proposal := createProposalWithObservations(t, svc, parcel, []uint{original.ID}, author)
	validated, err := svc.TransitionProposal(proposal.ID, dto.ProposalTransitionRequest{To: string(constants.ProposalValidated), Version: proposal.Version}, author)
	if err != nil {
		t.Fatalf("validate proposal: %v", err)
	}
	if _, err := svc.TransitionObservation(original.ID, dto.ObservationTransitionRequest{To: "rejected", Version: original.Version}, author); err != nil {
		t.Fatalf("reject observation: %v", err)
	}
	if _, err := svc.TransitionProposal(proposal.ID, dto.ProposalTransitionRequest{To: string(constants.ProposalSubmitted), Version: validated.Version}, author); err == nil {
		t.Fatalf("submit with rejected evidence unexpectedly succeeded")
	}

	updated, err := svc.UpdateProposalEvidence(proposal.ID, dto.ProposalEvidenceUpdateRequest{Version: validated.Version, ObservationIDs: []uint{fresh.ID}}, author)
	if err != nil {
		t.Fatalf("replace evidence: %v", err)
	}
	if updated.ProposalState != constants.ProposalValidated || updated.Version != validated.Version+1 {
		t.Fatalf("updated proposal = state %s v%d, want validated v%d", updated.ProposalState, updated.Version, validated.Version+1)
	}
	if len(updated.InvalidEvidence) != 0 {
		t.Fatalf("invalid evidence after replacement = %#v, want none", updated.InvalidEvidence)
	}
	submitted, err := svc.TransitionProposal(proposal.ID, dto.ProposalTransitionRequest{To: string(constants.ProposalSubmitted), Version: updated.Version}, author)
	if err != nil {
		t.Fatalf("submit after replacing evidence: %v", err)
	}
	reviewed, err := svc.TransitionProposal(proposal.ID, dto.ProposalTransitionRequest{To: string(constants.ProposalReviewed), Version: submitted.Version}, reviewer)
	if err != nil {
		t.Fatalf("review proposal: %v", err)
	}
	accepted, err := svc.TransitionProposal(proposal.ID, dto.ProposalTransitionRequest{To: string(constants.ProposalAccepted), Version: reviewed.Version}, reviewer)
	if err != nil {
		t.Fatalf("accept proposal: %v", err)
	}
	if accepted.ProposalState != constants.ProposalAccepted {
		t.Fatalf("accepted proposal state = %s", accepted.ProposalState)
	}
}

// Wrong parcel, already-void observations, non-author callers, and terminal
// states are rejected without changing the proposal.
func TestEvidenceReplacementValidation(t *testing.T) {
	svc, _ := newCadastralTestService(t)
	author := testActor(631, constants.RoleSurveyor, "author-evidence")
	other := testActor(632, constants.RoleGISAnalyst, "other-author")
	parcel := createTestParcel(t, svc, "P-EVIDENCE-MAIN", serviceTestPolygon(`[0,0],[10,0],[10,10],[0,10],[0,0]`), author)
	otherParcel := createTestParcel(t, svc, "P-EVIDENCE-OTHER", serviceTestPolygon(`[20,0],[30,0],[30,10],[20,10],[20,0]`), other)
	good := importTestObservation(t, svc, parcel.ID, "OBS-EV-GOOD", author)
	foreign := importTestObservation(t, svc, otherParcel.ID, "OBS-EV-FOREIGN", other)
	rejected := importTestObservation(t, svc, parcel.ID, "OBS-EV-REJECTED", author)
	if _, err := svc.TransitionObservation(rejected.ID, dto.ObservationTransitionRequest{To: "rejected", Version: rejected.Version}, author); err != nil {
		t.Fatalf("reject observation: %v", err)
	}
	proposal := createProposalWithObservations(t, svc, parcel, []uint{good.ID}, author)

	// Wrong parcel observations cannot be attached.
	if _, err := svc.UpdateProposalEvidence(proposal.ID, dto.ProposalEvidenceUpdateRequest{Version: proposal.Version, ObservationIDs: []uint{foreign.ID}}, author); err == nil {
		t.Fatalf("replace evidence with other-parcel observation unexpectedly succeeded")
	}
	// Already-void observations cannot be attached.
	if _, err := svc.UpdateProposalEvidence(proposal.ID, dto.ProposalEvidenceUpdateRequest{Version: proposal.Version, ObservationIDs: []uint{rejected.ID}}, author); err == nil {
		t.Fatalf("replace evidence with rejected observation unexpectedly succeeded")
	}
	// A different author cannot replace the evidence.
	_, err := svc.UpdateProposalEvidence(proposal.ID, dto.ProposalEvidenceUpdateRequest{Version: proposal.Version, ObservationIDs: []uint{good.ID}}, other)
	var appErr *AppError
	if !errors.As(err, &appErr) || appErr.Status != 403 {
		t.Fatalf("non-author evidence replacement error = %v, want 403", err)
	}
	// Stale versions are rejected and the record stays untouched.
	validated, err := svc.TransitionProposal(proposal.ID, dto.ProposalTransitionRequest{To: string(constants.ProposalValidated), Version: proposal.Version}, author)
	if err != nil {
		t.Fatalf("validate proposal: %v", err)
	}
	_, err = svc.UpdateProposalEvidence(proposal.ID, dto.ProposalEvidenceUpdateRequest{Version: proposal.Version, ObservationIDs: []uint{good.ID}}, author)
	assertConflictError(t, err, CodeConflict)

	// After the proposal reaches a terminal review state, evidence is frozen.
	reviewer := testActor(633, constants.RoleReviewer, "reviewer-evidence")
	submitted, err := svc.TransitionProposal(proposal.ID, dto.ProposalTransitionRequest{To: string(constants.ProposalSubmitted), Version: validated.Version}, author)
	if err != nil {
		t.Fatalf("submit proposal: %v", err)
	}
	reviewed, err := svc.TransitionProposal(proposal.ID, dto.ProposalTransitionRequest{To: string(constants.ProposalReviewed), Version: submitted.Version}, reviewer)
	if err != nil {
		t.Fatalf("review proposal: %v", err)
	}
	accepted, err := svc.TransitionProposal(proposal.ID, dto.ProposalTransitionRequest{To: string(constants.ProposalAccepted), Version: reviewed.Version}, reviewer)
	if err != nil {
		t.Fatalf("accept proposal: %v", err)
	}
	_, err = svc.UpdateProposalEvidence(proposal.ID, dto.ProposalEvidenceUpdateRequest{Version: accepted.Version, ObservationIDs: []uint{good.ID}}, author)
	assertConflictError(t, err, CodeConflict)
}

// Creating a proposal on already-void or foreign observations is rejected up
// front so evidence can never start life in an invalid state.
func TestCreateProposalRejectsVoidAndForeignObservations(t *testing.T) {
	svc, _ := newCadastralTestService(t)
	author := testActor(641, constants.RoleSurveyor, "author-create-evidence")
	other := testActor(642, constants.RoleSurveyor, "other-parcel-owner")
	parcel := createTestParcel(t, svc, "P-CREATE-MAIN", serviceTestPolygon(`[0,0],[10,0],[10,10],[0,10],[0,0]`), author)
	otherParcel := createTestParcel(t, svc, "P-CREATE-OTHER", serviceTestPolygon(`[20,0],[30,0],[30,10],[20,10],[20,0]`), other)
	rejected := importTestObservation(t, svc, parcel.ID, "OBS-CREATE-REJECTED", author)
	foreign := importTestObservation(t, svc, otherParcel.ID, "OBS-CREATE-FOREIGN", other)
	if _, err := svc.TransitionObservation(rejected.ID, dto.ObservationTransitionRequest{To: "rejected", Version: rejected.Version}, author); err != nil {
		t.Fatalf("reject observation: %v", err)
	}
	boundary := serviceTestPolygon(`[0,0],[10,0],[10,10],[0,10],[0,0]`)
	if _, err := svc.CreateProposal(dto.CreateProposalRequest{
		ParcelID: parcel.ID, BaseVersion: parcel.BoundaryVersion, ProposedGeoJSON: boundary,
		ObservationIDs: []uint{rejected.ID}, SnapToleranceM: 0.1,
	}, author); err == nil {
		t.Fatalf("create proposal on rejected observation unexpectedly succeeded")
	}
	var appErr *AppError
	if _, err := svc.CreateProposal(dto.CreateProposalRequest{
		ParcelID: parcel.ID, BaseVersion: parcel.BoundaryVersion, ProposedGeoJSON: boundary,
		ObservationIDs: []uint{foreign.ID}, SnapToleranceM: 0.1,
	}, author); !errors.As(err, &appErr) || appErr.Status != 400 {
		t.Fatalf("create proposal on foreign observation error = %v, want 400", err)
	}
}

// Reviewer-requested revisions let the author replace evidence and resubmit.
func TestRevisionFlowAllowsEvidenceReplacement(t *testing.T) {
	svc, _ := newCadastralTestService(t)
	author := testActor(651, constants.RoleSurveyor, "revision-author")
	reviewer := testActor(652, constants.RoleReviewer, "revision-reviewer")
	parcel := createTestParcel(t, svc, "P-REVISION-EVIDENCE", serviceTestPolygon(`[0,0],[10,0],[10,10],[0,10],[0,0]`), author)
	original := importTestObservation(t, svc, parcel.ID, "OBS-REVISION-OLD", author)
	fresh := importTestObservation(t, svc, parcel.ID, "OBS-REVISION-NEW", author)
	proposal := createProposalWithObservations(t, svc, parcel, []uint{original.ID}, author)
	validated, err := svc.TransitionProposal(proposal.ID, dto.ProposalTransitionRequest{To: string(constants.ProposalValidated), Version: proposal.Version}, author)
	if err != nil {
		t.Fatalf("validate proposal: %v", err)
	}
	submitted, err := svc.TransitionProposal(proposal.ID, dto.ProposalTransitionRequest{To: string(constants.ProposalSubmitted), Version: validated.Version}, author)
	if err != nil {
		t.Fatalf("submit proposal: %v", err)
	}
	if _, err := svc.TransitionObservation(original.ID, dto.ObservationTransitionRequest{To: "rejected", Version: original.Version}, author); err != nil {
		t.Fatalf("reject observation: %v", err)
	}
	reviewed, err := svc.TransitionProposal(proposal.ID, dto.ProposalTransitionRequest{To: string(constants.ProposalReviewed), Version: submitted.Version}, reviewer)
	if err != nil {
		t.Fatalf("review proposal: %v", err)
	}
	revision, err := svc.TransitionProposal(proposal.ID, dto.ProposalTransitionRequest{To: string(constants.ProposalRevision), Version: reviewed.Version}, reviewer)
	if err != nil {
		t.Fatalf("request revision: %v", err)
	}
	draft, err := svc.TransitionProposal(proposal.ID, dto.ProposalTransitionRequest{To: string(constants.ProposalDraft), Version: revision.Version}, author)
	if err != nil {
		t.Fatalf("return proposal to draft: %v", err)
	}
	updated, err := svc.UpdateProposalEvidence(proposal.ID, dto.ProposalEvidenceUpdateRequest{Version: draft.Version, ObservationIDs: []uint{fresh.ID}}, author)
	if err != nil {
		t.Fatalf("replace evidence after revision: %v", err)
	}
	if len(updated.InvalidEvidence) != 0 {
		t.Fatalf("invalid evidence = %#v, want none after replacement", updated.InvalidEvidence)
	}
}
