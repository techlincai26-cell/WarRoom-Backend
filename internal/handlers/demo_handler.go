package handlers

import (
	"encoding/base64"
	"io"
	"net/http"
	"war-room-backend/internal/services"

	"github.com/labstack/echo/v4"
)

// ============================================
// DEMO HANDLER — Public endpoints (no auth)
// ============================================

type DemoHandler struct {
	Service *services.DemoService
}

func NewDemoHandler(s *services.DemoService) *DemoHandler {
	return &DemoHandler{Service: s}
}

// ============================================
// Request Types
// ============================================

type GenerateScenarioRequest struct {
	Introduction      string `json:"introduction"`
	RoundNumber       int    `json:"roundNumber"`
	PreviousScenarios string `json:"previousScenarios"`
}

type EvaluateTextRequest struct {
	Introduction string `json:"introduction"`
	Question     string `json:"question"`
	Response     string `json:"response"`
}

type GenerateFollowupRequest struct {
	Introduction           string `json:"introduction"`
	OriginalQuestion       string `json:"originalQuestion"`
	SelectedOptionText     string `json:"selectedOptionText"`
	SelectedOptionFeedback string `json:"selectedOptionFeedback"`
	RoundNumber            int    `json:"roundNumber"`
}

type GeneratePitchRequest struct {
	Introduction string `json:"introduction"`
}

type GeneratePitchQnARequest struct {
	Introduction  string `json:"introduction"`
	PitchResponse string `json:"pitchResponse"`
	RoundNumber   int    `json:"roundNumber"`
}

type GenerateNegotiationRequest struct {
	Introduction     string `json:"introduction"`
	PitchResponse    string `json:"pitchResponse"`
	RoundNumber      int    `json:"roundNumber"`
	PreviousContext  string `json:"previousContext"`
}

type GenerateCompetencyReportRequest struct {
	Summary string `json:"summary"`
}

// ============================================
// POST /api/demo/generate-scenario
// ============================================

func (h *DemoHandler) GenerateScenario(c echo.Context) error {
	req := new(GenerateScenarioRequest)
	if err := c.Bind(req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid request"})
	}

	if req.Introduction == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Introduction is required"})
	}

	if req.RoundNumber < 1 || req.RoundNumber > 3 {
		req.RoundNumber = 1
	}

	scenario, err := h.Service.GenerateScenario(req.Introduction, req.RoundNumber, req.PreviousScenarios)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Failed to generate scenario: " + err.Error()})
	}

	return c.JSON(http.StatusOK, scenario)
}

// ============================================
// POST /api/demo/generate-followup
// ============================================

func (h *DemoHandler) GenerateFollowup(c echo.Context) error {
	req := new(GenerateFollowupRequest)
	if err := c.Bind(req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid request"})
	}

	if req.Introduction == "" || req.OriginalQuestion == "" || req.SelectedOptionText == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "introduction, originalQuestion, and selectedOptionText are required"})
	}

	if req.RoundNumber < 1 || req.RoundNumber > 3 {
		req.RoundNumber = 1
	}

	followup, err := h.Service.GenerateFollowupScenario(
		req.Introduction,
		req.OriginalQuestion,
		req.SelectedOptionText,
		req.SelectedOptionFeedback,
		req.RoundNumber,
	)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Failed to generate follow-up: " + err.Error()})
	}

	return c.JSON(http.StatusOK, followup)
}

// ============================================
// POST /api/demo/generate-pitch
// ============================================

func (h *DemoHandler) GeneratePitch(c echo.Context) error {
	req := new(GeneratePitchRequest)
	if err := c.Bind(req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid request"})
	}

	if req.Introduction == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Introduction is required"})
	}

	pitch, err := h.Service.GeneratePitchScenario(req.Introduction)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Failed to generate pitch scenario"})
	}

	return c.JSON(http.StatusOK, pitch)
}

// ============================================
// POST /api/demo/generate-pitch-qna
// ============================================

func (h *DemoHandler) GeneratePitchQnA(c echo.Context) error {
	req := new(GeneratePitchQnARequest)
	if err := c.Bind(req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid request"})
	}

	if req.Introduction == "" || req.PitchResponse == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Introduction and pitchResponse are required"})
	}

	qna, err := h.Service.GeneratePitchQnAScenario(req.Introduction, req.PitchResponse, req.RoundNumber)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Failed to generate pitch Q&A scenario: " + err.Error()})
	}

	return c.JSON(http.StatusOK, qna)
}

// ============================================
// POST /api/demo/generate-negotiation
// ============================================

func (h *DemoHandler) GenerateNegotiation(c echo.Context) error {
	req := new(GenerateNegotiationRequest)
	if err := c.Bind(req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid request"})
	}

	if req.Introduction == "" || req.PitchResponse == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Introduction and pitchResponse are required"})
	}

	negotiation, err := h.Service.GenerateNegotiationScenario(req.Introduction, req.PitchResponse, req.RoundNumber, req.PreviousContext)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Failed to generate negotiation scenario: " + err.Error()})
	}

	return c.JSON(http.StatusOK, negotiation)
}

// ============================================
// POST /api/demo/generate-competency-report
// ============================================

func (h *DemoHandler) GenerateCompetencyReport(c echo.Context) error {
	req := new(GenerateCompetencyReportRequest)
	if err := c.Bind(req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid request"})
	}

	if req.Summary == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Summary is required"})
	}

	report, err := h.Service.GenerateCompetencyReport(req.Summary)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Failed to generate competency report: " + err.Error()})
	}

	return c.JSON(http.StatusOK, report)
}

// ============================================
// POST /api/demo/evaluate-response
// ============================================

func (h *DemoHandler) EvaluateResponse(c echo.Context) error {
	req := new(EvaluateTextRequest)
	if err := c.Bind(req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid request"})
	}

	if req.Response == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Response is required"})
	}

	eval, err := h.Service.EvaluateTextResponse(req.Introduction, req.Question, req.Response)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Failed to evaluate response"})
	}

	return c.JSON(http.StatusOK, eval)
}

// ============================================
// POST /api/demo/evaluate-voice
// ============================================

func (h *DemoHandler) EvaluateVoice(c echo.Context) error {
	introduction := c.FormValue("introduction")
	question := c.FormValue("question")

	if introduction == "" || question == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "introduction and question are required"})
	}

	// Read audio file
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

	eval, err := h.Service.EvaluateVoiceResponse(introduction, question, audioBase64, mimeType)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Failed to evaluate voice response"})
	}

	return c.JSON(http.StatusOK, eval)
}
