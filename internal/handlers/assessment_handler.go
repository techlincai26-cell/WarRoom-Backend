package handlers

import (
	"encoding/json"
	"net/http"
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
	user := c.Get("user").(*jwt.Token)
	claims := user.Claims.(jwt.MapClaims)
	return claims["user_id"].(string)
}

// ============================================
// CRUD Endpoints
// ============================================

// POST /assessments - Create new assessment
func (h *AssessmentHandler) Create(c echo.Context) error {
	userID := getUserID(c)

	var req json.RawMessage
	if err := c.Bind(&req); err != nil {
		req = json.RawMessage(`{}`)
	}

	assessment, err := h.Service.CreateAssessment(userID, req)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Failed to create assessment"})
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

// ============================================
// Report
// ============================================

// GET /assessments/:id/report - Generate or get evaluation report
func (h *AssessmentHandler) GetReport(c echo.Context) error {
	assessmentID := c.Param("id")

	report, err := h.Service.GenerateReport(assessmentID)
	if err != nil {
		if err.Error() == "assessment not found" {
			return c.JSON(http.StatusNotFound, map[string]string{"error": err.Error()})
		}
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Failed to generate report"})
	}

	return c.JSON(http.StatusOK, report)
}
