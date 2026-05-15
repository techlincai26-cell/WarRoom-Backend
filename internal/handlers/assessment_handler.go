package handlers

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"
	"war-room-backend/internal/models"
	"war-room-backend/internal/services"

	"github.com/golang-jwt/jwt/v5"
	"github.com/labstack/echo/v4"
)

type AssessmentHandler struct {
	Service *services.AssessmentService
}

func NewAssessmentHandler(s *services.AssessmentService) *AssessmentHandler {
	return &AssessmentHandler{Service: s}
}

// ============================================
// Request types
// ============================================

type SubmitResponseRequest struct {
	QuestionID   string          `json:"questionId"`
	ResponseData json.RawMessage `json:"responseData"`
}

type SubmitStageResponsesRequest struct {
	Responses map[string]json.RawMessage `json:"responses"`
}

// PhaseResponseItem is a single answer from the frontend array format.
type PhaseResponseItem struct {
	QuestionID       string             `json:"questionId"`
	Type             string             `json:"type"`
	Text             string             `json:"text,omitempty"`
	SelectedOptionID string             `json:"selectedOptionId,omitempty"`
	Allocations      map[string]float64 `json:"allocations,omitempty"`
}

// PhaseSubmitRequest collects all answers for a full phase.
type PhaseSubmitRequest struct {
	StageID   string              `json:"stageId"`
	Responses []PhaseResponseItem `json:"responses"` // array of response items from frontend
}

// CharactersRequest for setting chosen mentors/leaders/investors.
type CharactersRequest struct {
	SelectedMentors   []string `json:"selectedMentors"`   // 3 mentor IDs
	SelectedLeaders   []string `json:"selectedLeaders"`   // 3 leader IDs
	SelectedInvestors []string `json:"selectedInvestors"` // 3 investor IDs
}

// PhaseScenarioRequest for submitting the leader scenario answer.
type PhaseScenarioRequest struct {
	FromStage string `json:"fromStage"`
	ToStage   string `json:"toStage"`
	Response  string `json:"response"`
}

type MentorLifelineRequest struct {
	MentorID string `json:"mentorId"`
	Question string `json:"question"`
}

type SubmitPitchRequest struct {
	PitchText string `json:"pitchText"`
}

type InvestorResponseRequest struct {
	InvestorID string `json:"investorId"`
	Response   string `json:"response"`
}

// ============================================
// Helper: extract user ID from JWT
// ============================================

func getUserID(c echo.Context) string {
	userToken, ok := c.Get("user").(*jwt.Token)
	if !ok || userToken == nil {
		return ""
	}

	claims, ok := userToken.Claims.(jwt.MapClaims)
	if !ok {
		return ""
	}

	// Frontend may provide "userId", existing backend used "user_id"
	if id, ok := claims["userId"].(string); ok && id != "" {
		return id
	}
	if id, ok := claims["user_id"].(string); ok && id != "" {
		return id
	}
	if id, ok := claims["sub"].(string); ok && id != "" {
		return id
	}
	return ""
}

// ============================================
// CRUD Endpoints
// ============================================

// POST /assessments - Create new assessment
func (h *AssessmentHandler) Create(c echo.Context) error {
	userID := getUserID(c)
	if userID == "" {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "Unauthorized: missing user ID in token"})
	}

	// Guard: verify the user's batch is still active before allowing a new assessment.
	if err := h.Service.CheckBatchActive(userID); err != nil {
		if err.Error() == "batch_disabled" {
			return c.JSON(http.StatusForbidden, map[string]string{
				"error": "Your batch has been disabled by the admin. Please contact your instructor to re-enable it before starting a new simulation.",
			})
		}
		// Any other DB error — don't block, just log and proceed.
		// (We'd rather allow than hard-fail on a lookup error during local dev.)
	}

	var req json.RawMessage
	if err := c.Bind(&req); err != nil {
		req = json.RawMessage(`{}`)
	}

	assessment, err := h.Service.CreateAssessment(userID, req)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Failed to create assessment: " + err.Error()})
	}

	return c.JSON(http.StatusCreated, assessment)
}

// GET /assessments - List user's assessments
func (h *AssessmentHandler) List(c echo.Context) error {
	userID := getUserID(c)

	assessments, err := h.Service.ListAssessments(userID)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Failed to list assessments"})
	}
	return c.JSON(http.StatusOK, assessments)
}

// GET /assessments/:id - Get assessment state
func (h *AssessmentHandler) Get(c echo.Context) error {
	assessmentID := c.Param("id")

	state, err := h.Service.GetAssessment(assessmentID)
	if err != nil {
		if err.Error() == "assessment not found" {
			return c.JSON(http.StatusNotFound, map[string]string{"error": "Assessment not found"})
		}
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Failed to get assessment"})
	}

	return c.JSON(http.StatusOK, state)
}

// ============================================
// Response Endpoints
// ============================================

// POST /assessments/:id/responses - Submit response to current question
func (h *AssessmentHandler) SubmitResponse(c echo.Context) error {
	assessmentID := c.Param("id")

	req := new(SubmitResponseRequest)
	if err := c.Bind(req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid request"})
	}

	result, err := h.Service.SubmitResponse(assessmentID, req.QuestionID, req.ResponseData)
	if err != nil {
		switch err.Error() {
		case "assessment not found":
			return c.JSON(http.StatusNotFound, map[string]string{"error": err.Error()})
		case "invalid question ID":
			return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		default:
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Failed to submit response"})
		}
	}

	return c.JSON(http.StatusOK, result)
}

// POST /assessments/:id/stage-responses - Submit responses for the entire stage
func (h *AssessmentHandler) SubmitStageResponses(c echo.Context) error {
	assessmentID := c.Param("id")

	req := new(SubmitStageResponsesRequest)
	if err := c.Bind(req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid request"})
	}

	result, err := h.Service.SubmitStageResponses(assessmentID, req.Responses)
	if err != nil {
		switch err.Error() {
		case "assessment not found":
			return c.JSON(http.StatusNotFound, map[string]string{"error": err.Error()})
		case "invalid stage ID":
			return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		default:
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Failed to submit stage responses"})
		}
	}

	return c.JSON(http.StatusOK, result)
}

// ============================================
// Mentor Lifeline
// ============================================

// POST /assessments/:id/mentor - Use mentor lifeline
func (h *AssessmentHandler) UseMentorLifeline(c echo.Context) error {
	assessmentID := c.Param("id")

	req := new(MentorLifelineRequest)
	if err := c.Bind(req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid request"})
	}

	result, err := h.Service.UseMentorLifeline(assessmentID, req.MentorID, req.Question)
	if err != nil {
		switch err.Error() {
		case "assessment not found":
			return c.JSON(http.StatusNotFound, map[string]string{"error": err.Error()})
		case "no mentor lifelines remaining":
			return c.JSON(http.StatusForbidden, map[string]string{"error": err.Error()})
		case "invalid mentor ID":
			return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		default:
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Failed to use mentor lifeline"})
		}
	}

	return c.JSON(http.StatusOK, result)
}

// ============================================
// War Room Endpoints
// ============================================

// POST /assessments/:id/warroom/pitch - Submit War Room pitch
func (h *AssessmentHandler) SubmitPitch(c echo.Context) error {
	assessmentID := c.Param("id")

	req := new(SubmitPitchRequest)
	if err := c.Bind(req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid request"})
	}

	result, err := h.Service.SubmitPitch(assessmentID, req.PitchText)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Failed to submit pitch"})
	}

	return c.JSON(http.StatusOK, result)
}

// POST /assessments/:id/warroom/respond - Respond to investor question
func (h *AssessmentHandler) RespondToInvestor(c echo.Context) error {
	assessmentID := c.Param("id")

	req := new(InvestorResponseRequest)
	if err := c.Bind(req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid request"})
	}

	scorecard, err := h.Service.RespondToInvestor(assessmentID, req.InvestorID, req.Response)
	if err != nil {
		switch err.Error() {
		case "assessment not found":
			return c.JSON(http.StatusNotFound, map[string]string{"error": err.Error()})
		case "invalid investor ID":
			return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		default:
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Failed to process investor response"})
		}
	}

	return c.JSON(http.StatusOK, scorecard)
}

// GET /assessments/:id/warroom/scorecard - Get investor scorecards
func (h *AssessmentHandler) GetScorecard(c echo.Context) error {
	assessmentID := c.Param("id")

	scorecards, err := h.Service.GetScorecards(assessmentID)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Failed to get scorecard"})
	}

	return c.JSON(http.StatusOK, scorecards)
}

// GET /assessments/:id/warroom/offers - Get negotiation offers
func (h *AssessmentHandler) GetWarRoomOffers(c echo.Context) error {
	assessmentID := c.Param("id")

	offers, err := h.Service.GetNegotiationOffers(assessmentID)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}

	return c.JSON(http.StatusOK, offers)
}

type CounterNegotiateRequest struct {
	InvestorID string  `json:"investorId"`
	Capital    float64 `json:"capital"`
	Equity     float64 `json:"equity"`
}

type CounterNegotiateAudioRequest struct {
	InvestorID string `form:"investorId"`
}

// POST /assessments/:id/warroom/counter - Counter-negotiate with investor
func (h *AssessmentHandler) CounterNegotiate(c echo.Context) error {
	assessmentID := c.Param("id")
	req := new(CounterNegotiateRequest)
	if err := c.Bind(req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid request"})
	}

	result, err := h.Service.CounterNegotiate(assessmentID, req.InvestorID, req.Capital, req.Equity)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}

	return c.JSON(http.StatusOK, result)
}

// POST /assessments/:id/warroom/counter-audio - Counter-negotiate with investor using voice
func (h *AssessmentHandler) CounterNegotiateAudio(c echo.Context) error {
	assessmentID := c.Param("id")
	investorID := c.FormValue("investorId")

	file, err := c.FormFile("audio")
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Audio file required"})
	}

	src, err := file.Open()
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Failed to open audio"})
	}
	defer src.Close()

	audioData, err := io.ReadAll(src)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Failed to read audio"})
	}

	audioBase64 := base64.StdEncoding.EncodeToString(audioData)

	result, err := h.Service.CounterNegotiateAudio(assessmentID, investorID, audioBase64)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}

	return c.JSON(http.StatusOK, result)
}

// ============================================
// Report
// ============================================

// GET /assessments/:id/report - Generate or get evaluation report.
// Accepts an optional `?regenerate=true` query param that drops any cached report
// and rebuilds from current assessment state. Use this after the user changes
// answers post-buyout, or any time the cached report becomes stale.
func (h *AssessmentHandler) GetReport(c echo.Context) error {
	assessmentID := c.Param("id")

	regenerate := strings.EqualFold(c.QueryParam("regenerate"), "true")

	var report *models.Report
	var err error
	if regenerate {
		report, err = h.Service.RegenerateReport(assessmentID)
	} else {
		report, err = h.Service.GenerateReport(assessmentID)
	}
	if err != nil {
		if err.Error() == "assessment not found" {
			return c.JSON(http.StatusNotFound, map[string]string{"error": err.Error()})
		}
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Failed to generate report"})
	}

	return c.JSON(http.StatusOK, report)
}

// ============================================
// Phase Submit (new v2 flow)
// ============================================

// POST /assessments/:id/phase-submit
// Accepts all answers for a phase, auto-scores MCQ, queues AI for open text.
func (h *AssessmentHandler) SubmitPhase(c echo.Context) error {
	assessmentID := c.Param("id")
	req := new(PhaseSubmitRequest)
	if err := c.Bind(req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid request: " + err.Error()})
	}

	// Convert array of responses into map[questionId]json.RawMessage for the service
	responsesMap := make(map[string]json.RawMessage)
	for _, r := range req.Responses {
		raw, _ := json.Marshal(r)
		responsesMap[r.QuestionID] = raw
	}

	result, err := h.Service.SubmitPhase(assessmentID, req.StageID, responsesMap)
	if err != nil {
		switch err.Error() {
		case "assessment not found":
			return c.JSON(http.StatusNotFound, map[string]string{"error": err.Error()})
		case "stage mismatch":
			return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		default:
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Failed to submit phase: " + err.Error()})
		}
	}

	return c.JSON(http.StatusOK, result)
}

// ============================================
// Character Selection
// ============================================

// GET /assessments/:id/characters
func (h *AssessmentHandler) GetCharacters(c echo.Context) error {
	assessmentID := c.Param("id")
	chars, err := h.Service.GetCharacters(assessmentID)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, chars)
}

// POST /assessments/:id/characters
func (h *AssessmentHandler) SetCharacters(c echo.Context) error {
	assessmentID := c.Param("id")
	req := new(CharactersRequest)
	if err := c.Bind(req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid request"})
	}
	if len(req.SelectedMentors) != 2 || len(req.SelectedLeaders) != 2 || len(req.SelectedInvestors) != 4 {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Must select exactly 2 mentors, 2 leaders, and 4 investors"})
	}
	if err := h.Service.SetCharacters(assessmentID, req.SelectedMentors, req.SelectedLeaders, req.SelectedInvestors); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, map[string]string{"message": "Characters saved"})
}

// ============================================
// Phase Scenario
// ============================================

// POST /assessments/:id/phase-scenario
func (h *AssessmentHandler) AnswerPhaseScenario(c echo.Context) error {
	assessmentID := c.Param("id")
	req := new(PhaseScenarioRequest)
	if err := c.Bind(req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid request"})
	}

	result, err := h.Service.AnswerPhaseScenario(assessmentID, req.FromStage, req.ToStage, req.Response)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, result)
}

// ============================================
// AI End-of-Phase Question
// ============================================

type GenerateAiQuestionRequest struct {
	StageID   string `json:"stageId"`
	Responses []struct {
		QuestionID string `json:"questionId"`
		Summary    string `json:"summary"`
	} `json:"responses"`
	UserIdea string `json:"userIdea"`
}

// POST /assessments/:id/generate-ai-question
func (h *AssessmentHandler) GenerateAiQuestion(c echo.Context) error {
	assessmentID := c.Param("id")
	req := new(GenerateAiQuestionRequest)
	if err := c.Bind(req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid request"})
	}

	svcReq := &services.GenerateAiQuestionRequest{
		StageID:  req.StageID,
		UserIdea: req.UserIdea,
	}
	for _, r := range req.Responses {
		svcReq.Responses = append(svcReq.Responses, struct {
			QuestionID string `json:"questionId"`
			Summary    string `json:"summary"`
		}{QuestionID: r.QuestionID, Summary: r.Summary})
	}

	result, err := h.Service.GenerateAiQuestion(assessmentID, svcReq)
	if err != nil {
		if err.Error() == "assessment not found" {
			return c.JSON(http.StatusNotFound, map[string]string{"error": err.Error()})
		}
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Failed to generate AI question"})
	}
	return c.JSON(http.StatusOK, result)
}

// ============================================
// DYNAMIC SCENARIO HANDLING
// ============================================

// GET /assessments/:id/dynamic-scenario?stageId=X&questionId=Y
func (h *AssessmentHandler) GetDynamicScenario(c echo.Context) error {
	assessmentID := c.Param("id")
	stageID := c.QueryParam("stageId")
	questionID := c.QueryParam("questionId")

	if stageID == "" || questionID == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "stageId and questionId are required"})
	}

	scenario, err := h.Service.GetDynamicScenario(assessmentID, stageID, questionID)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Failed to get dynamic scenario: " + err.Error()})
	}

	return c.JSON(http.StatusOK, scenario)
}

// GET /api/assessments/:id/stage/:stageId/dynamic-scenarios
func (h *AssessmentHandler) GetStageDynamicScenarios(c echo.Context) error {
	assessmentID := c.Param("id")
	stageID := c.Param("stageId")

	if stageID == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "stageId is required"})
	}

	scenarios, err := h.Service.GetStageDynamicScenarios(assessmentID, stageID)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Failed to get dynamic scenarios: " + err.Error()})
	}

	return c.JSON(http.StatusOK, scenarios)
}

type SubmitDynamicScenarioRequest struct {
	ScenarioID       string `json:"scenarioId"`
	SelectedOptionID string `json:"selectedOptionId"`
}

// POST /assessments/:id/dynamic-scenario/submit
func (h *AssessmentHandler) SubmitDynamicScenario(c echo.Context) error {
	assessmentID := c.Param("id")
	req := new(SubmitDynamicScenarioRequest)
	if err := c.Bind(req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid request"})
	}

	result, err := h.Service.SubmitDynamicScenario(assessmentID, req.ScenarioID, req.SelectedOptionID)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Failed to submit dynamic scenario: " + err.Error()})
	}

	return c.JSON(http.StatusOK, result)
}

// RestartRequest is the optional body for POST /assessments/:id/restart.
// Both fields are optional — an empty body preserves the legacy behaviour
// (mode=month_zero, full wipe). Pass mode="continue" with an optional
// targetStage to revisit a specific phase with prior answers preserved.
type RestartRequest struct {
	Mode        string `json:"mode"`
	TargetStage string `json:"targetStage"`
}

// POST /assessments/:id/restart
func (h *AssessmentHandler) Restart(c echo.Context) error {
	assessmentID := c.Param("id")

	req := RestartRequest{}
	// Body is optional — ignore decode errors so existing callers that POST with
	// no body continue to work (they get the default month_zero restart).
	_ = c.Bind(&req)

	result, err := h.Service.RestartAssessment(assessmentID, services.RestartOpts{
		Mode:        req.Mode,
		TargetStage: req.TargetStage,
	})
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, result)
}

// POST /assessments/:id/warroom/accept-deal
func (h *AssessmentHandler) AcceptDeal(c echo.Context) error {
	assessmentID := c.Param("id")
	req := new(CounterNegotiateRequest)
	if err := c.Bind(req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid request format"})
	}

	assessment, err := h.Service.AcceptDeal(assessmentID, req.InvestorID, req.Capital, req.Equity)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}

	return c.JSON(http.StatusOK, assessment)
}

type RejectOfferRequest struct {
	OfferID string `json:"offerId"`
}

// POST /assessments/:id/warroom/reject-offer
func (h *AssessmentHandler) RejectOffer(c echo.Context) error {
	assessmentID := c.Param("id")
	req := new(RejectOfferRequest)
	if err := c.Bind(req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid request format"})
	}

	err := h.Service.RejectOffer(assessmentID, req.OfferID)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}

	return c.JSON(http.StatusOK, map[string]string{"status": "rejected"})
}

type BuyoutRequest struct {
	Company string  `json:"company"`
	Amount  float64 `json:"amount"`
}

// POST /assessments/:id/buyout
func (h *AssessmentHandler) HandleBuyout(c echo.Context) error {
	assessmentID := c.Param("id")
	var req BuyoutRequest
	if err := c.Bind(&req); err != nil {
		log.Printf("invalid buyout req %v", err)
	}
	result, err := h.Service.HandleBuyout(assessmentID, req.Company, req.Amount)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, result)
}

// POST /assessments/:id/walkout
func (h *AssessmentHandler) HandleWalkout(c echo.Context) error {
	assessmentID := c.Param("id")
	result, err := h.Service.HandleWalkout(assessmentID)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, result)
}

// ============================================
// War Room Audio Endpoints
// ============================================

// POST /assessments/:id/warroom/pitch-audio
func (h *AssessmentHandler) SubmitPitchAudio(c echo.Context) error {
	assessmentID := c.Param("id")

	// Read audio file from multipart form
	file, err := c.FormFile("audio")
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Audio file is required"})
	}

	src, err := file.Open()
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Failed to read audio file"})
	}
	defer src.Close()

	audioBytes, err := io.ReadAll(src)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Failed to read audio data"})
	}

	audioBase64 := base64.StdEncoding.EncodeToString(audioBytes)

	mimeType := file.Header.Get("Content-Type")
	if mimeType == "" {
		mimeType = "audio/webm"
	}

	result, err := h.Service.SubmitPitchAudio(assessmentID, audioBase64, mimeType)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}

	return c.JSON(http.StatusOK, result)
}

// POST /assessments/:id/warroom/respond-audio
func (h *AssessmentHandler) RespondToInvestorAudio(c echo.Context) error {
	assessmentID := c.Param("id")

	investorID := c.FormValue("investorId")
	if investorID == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "investorId is required"})
	}

	// Read audio file from multipart form
	file, err := c.FormFile("audio")
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Audio file is required"})
	}

	src, err := file.Open()
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Failed to read audio file"})
	}
	defer src.Close()

	audioBytes, err := io.ReadAll(src)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Failed to read audio data"})
	}

	audioBase64 := base64.StdEncoding.EncodeToString(audioBytes)

	mimeType := file.Header.Get("Content-Type")
	if mimeType == "" {
		mimeType = "audio/webm"
	}

	scorecard, analysisData, err := h.Service.RespondToInvestorAudio(assessmentID, investorID, audioBase64, mimeType)
	if err != nil {
		switch err.Error() {
		case "assessment not found":
			return c.JSON(http.StatusNotFound, map[string]string{"error": err.Error()})
		case "invalid investor ID":
			return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		default:
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Failed to process audio response"})
		}
	}

	// Merge scorecard with analysis data
	response := map[string]interface{}{
		"scorecard":     scorecard,
		"transcription": analysisData["transcription"],
		"audioBase64":   analysisData["audioBase64"],
	}

	return c.JSON(http.StatusOK, response)
}

func (h *AssessmentHandler) GenerateInvestorFollowupAudio(c echo.Context) error {
	assessmentID := c.Param("id")
	investorID := c.FormValue("investorId")
	if investorID == "" {
		return c.JSON(400, map[string]string{"error": "investorId required"})
	}

	file, err := c.FormFile("audio")
	if err != nil {
		return c.JSON(400, map[string]string{"error": "audio required"})
	}

	src, _ := file.Open()
	defer src.Close()
	audioBytes, _ := io.ReadAll(src)
	audioBase64 := base64.StdEncoding.EncodeToString(audioBytes)

	mimeType := file.Header.Get("Content-Type")
	if mimeType == "" {
		mimeType = "audio/webm"
	}

	data, err := h.Service.GenerateInvestorFollowupAudio(assessmentID, investorID, audioBase64, mimeType)
	if err != nil {
		return c.JSON(500, map[string]string{"error": err.Error()})
	}

	return c.JSON(200, data)
}

func (h *AssessmentHandler) RespondToInvestorFinalAudio(c echo.Context) error {
	assessmentID := c.Param("id")
	investorID := c.FormValue("investorId")
	initialTrans := c.FormValue("initialTranscription")
	followupQ := c.FormValue("followupQuestion")

	if investorID == "" || initialTrans == "" || followupQ == "" {
		return c.JSON(400, map[string]string{"error": "missing fields"})
	}

	file, _ := c.FormFile("audio")
	src, _ := file.Open()
	defer src.Close()
	audioBytes, _ := io.ReadAll(src)
	audioBase64 := base64.StdEncoding.EncodeToString(audioBytes)
	mimeType := file.Header.Get("Content-Type")
	if mimeType == "" {
		mimeType = "audio/webm"
	}

	scorecard, analysisData, err := h.Service.RespondToInvestorFinalAudio(assessmentID, investorID, initialTrans, followupQ, audioBase64, mimeType)
	if err != nil {
		return c.JSON(500, map[string]string{"error": err.Error()})
	}

	analysisData["scorecard"] = scorecard
	return c.JSON(200, analysisData)
}
