package services

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"war-room-backend/internal/db"
	"war-room-backend/internal/models"

	"github.com/google/uuid"
)

// ============================================
// ASSESSMENT SERVICE - SOP 2.0
// ============================================

type AssessmentService struct {
	DataManager   *DataManager
	AIService     *AIService
	ScoringEngine *ScoringEngine
}

func NewAssessmentService(dm *DataManager) *AssessmentService {
	ai := NewAIService()
	se := NewScoringEngine(dm.Config)
	return &AssessmentService{
		DataManager:   dm,
		AIService:     ai,
		ScoringEngine: se,
	}
}

// ============================================
// CREATE ASSESSMENT
// ============================================

type CreateAssessmentRequest struct {
	Level             int      `json:"level"` // 1 or 2
	UserIdea          string   `json:"userIdea,omitempty"`
	BatchCode         string   `json:"batchCode,omitempty"`
	SelectedMentors   []string `json:"selectedMentors,omitempty"`
	SelectedLeaders   []string `json:"selectedLeaders,omitempty"`
	SelectedInvestors []string `json:"selectedInvestors,omitempty"`
}

func (s *AssessmentService) CreateAssessment(userID string, setupData json.RawMessage) (*models.Assessment, error) {
	var req CreateAssessmentRequest
	if len(setupData) > 0 {
		json.Unmarshal(setupData, &req)
	}

	if req.Level == 0 {
		req.Level = 1 // Default to Student level
	}

	// Batch code — prefer from request, otherwise load from user record
	batchCode := req.BatchCode
	if batchCode == "" {
		var user models.User
		if err := db.DB.Where("id = ?", userID).First(&user).Error; err == nil {
			batchCode = user.BatchCode
		}
	}

	firstStage := "STAGE_NEG2_IDEATION"
	firstQ := s.DataManager.GetFirstQuestionInStage(firstStage)

	now := time.Now()

	var mentorsJSON, leadersJSON, investorsJSON json.RawMessage
	if len(req.SelectedMentors) > 0 {
		mentorsJSON, _ = json.Marshal(req.SelectedMentors)
	}
	if len(req.SelectedLeaders) > 0 {
		leadersJSON, _ = json.Marshal(req.SelectedLeaders)
	}
	if len(req.SelectedInvestors) > 0 {
		investorsJSON, _ = json.Marshal(req.SelectedInvestors)
	}

	assessment := &models.Assessment{
		ID:                       uuid.New().String(),
		UserID:                   userID,
		Level:                    req.Level,
		AttemptNumber:            1,
		Status:                   "IN_PROGRESS",
		BatchCode:                batchCode,
		SelectedMentors:          mentorsJSON,
		SelectedLeaders:          leadersJSON,
		SelectedInvestors:        investorsJSON,
		CurrentStage:             firstStage,
		CurrentQuestionID:        firstQ,
		SimulatedMonth:           0,
		MentorLifelinesRemaining: 3,
		RevenueProjection:        100000, // Start value ₹1L
		StartedAt:                &now,
		LastActiveAt:             &now,
		FinancialState:           json.RawMessage(`{"capital": 0, "revenue": 0, "burnRate": 0, "runway": 0, "equity": 100, "debt": 0}`),
		TeamState:                json.RawMessage(`{"size": 1, "morale": 100, "roles": ["founder"]}`),
		CustomerState:            json.RawMessage(`{"count": 0, "retention": 0, "satisfaction": 0}`),
		ProductState:             json.RawMessage(`{"quality": 0, "features": 0, "mvpLaunched": false}`),
		MarketState:              json.RawMessage(`{"competition": "unknown", "positioning": "undefined"}`),
	}

	if req.UserIdea != "" {
		assessment.UserIdea = req.UserIdea
	}

	if err := db.DB.Create(assessment).Error; err != nil {
		return nil, fmt.Errorf("failed to create assessment: %w", err)
	}

	// Create the first stage record
	stageData := s.DataManager.GetStage(firstStage)
	stageRecord := &models.Stage{
		ID:           uuid.New().String(),
		AssessmentID: assessment.ID,
		StageName:    firstStage,
		StageNumber:  stageData.StageNumber,
		StartedAt:    &now,
	}
	db.DB.Create(stageRecord)

	// Initialize competency scores for all 8 competencies
	for _, comp := range s.DataManager.Config.Competencies {
		cs := &models.CompetencyScore{
			ID:              uuid.New().String(),
			AssessmentID:    assessment.ID,
			CompetencyCode:  comp.Code,
			CompetencyName:  comp.Name,
			StageScores:     json.RawMessage(`{}`),
			WeightedAverage: 0,
			Category:        "DEVELOPMENT_REQUIRED",
		}
		db.DB.Create(cs)
	}

	log.Printf("[Assessment] Created: id=%s, user=%s, level=%d, stage=%s", assessment.ID, userID, req.Level, firstStage)
	return assessment, nil
}

// ============================================
// SUBMIT RESPONSE
// ============================================

type SubmitResponseResult struct {
	ResponseID     string            `json:"responseId"`
	AIEvaluation   json.RawMessage   `json:"aiEvaluation"`
	Proficiency    int               `json:"proficiency"`
	NextQuestion   *NextQuestionInfo `json:"nextQuestion,omitempty"`
	StageCompleted bool              `json:"stageCompleted"`
	NextStage      *NextStageInfo    `json:"nextStage,omitempty"`
	SimCompleted   bool              `json:"simCompleted"`
	StateUpdates   json.RawMessage   `json:"stateUpdates,omitempty"`
}

type NextQuestionInfo struct {
	QID  string          `json:"qId"`
	Data json.RawMessage `json:"data"`
}

type NextStageInfo struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Title         string   `json:"title"`
	StageNumber   int      `json:"stageNumber"`
	Competencies  []string `json:"competencies"`
	FirstQuestion string   `json:"firstQuestion"`
}

func (s *AssessmentService) SubmitResponse(assessmentID string, questionID string, responseData json.RawMessage) (*SubmitResponseResult, error) {
	// 1. Verify assessment exists and is in progress
	var assessment models.Assessment
	if err := db.DB.Where("id = ?", assessmentID).First(&assessment).Error; err != nil {
		return nil, errors.New("assessment not found")
	}
	if assessment.Status != "IN_PROGRESS" {
		return nil, errors.New("assessment is not in progress")
	}

	// 2. Get question data
	question := s.DataManager.GetQuestion(questionID)
	if question == nil {
		return nil, errors.New("invalid question ID")
	}

	stageID := s.DataManager.QuestionStageMap[questionID]

	// 3. Get or create stage record
	var stageRecord models.Stage
	if err := db.DB.Where("assessmentId = ? AND stageName = ?", assessmentID, stageID).First(&stageRecord).Error; err != nil {
		now := time.Now()
		stageData := s.DataManager.GetStage(stageID)
		stageRecord = models.Stage{
			ID:           uuid.New().String(),
			AssessmentID: assessmentID,
			StageName:    stageID,
			StageNumber:  stageData.StageNumber,
			StartedAt:    &now,
		}
		db.DB.Create(&stageRecord)
	}

	// 4. Evaluate response
	var proficiency int
	var aiEvalJSON json.RawMessage

	// Parse response data
	var respMap map[string]interface{}
	json.Unmarshal(responseData, &respMap)

	selectedOptionID := ""
	responseText := ""
	if val, ok := respMap["selectedOptionId"].(string); ok {
		selectedOptionID = val
	}
	if val, ok := respMap["text"].(string); ok {
		responseText = val
	}

	switch question.Type {
	case "open_text":
		// Use AI to evaluate
		compDefs := s.DataManager.GetCompetencyDefs()
		eval, err := s.AIService.EvaluateOpenText(
			question.Text,
			responseText,
			question.Assess,
			compDefs,
		)
		if err != nil {
			log.Printf("[Assessment] AI eval error: %v", err)
			eval = &TextEvaluation{Proficiency: 2, Feedback: "Response recorded."}
		}
		proficiency = eval.Proficiency
		aiEvalJSON, _ = json.Marshal(eval)

	case "multiple_choice", "scenario":
		// Score based on selected option
		for _, opt := range question.Options {
			if opt.ID == selectedOptionID {
				proficiency = opt.Proficiency
				aiEvalJSON, _ = json.Marshal(map[string]interface{}{
					"proficiency": proficiency,
					"signal":      opt.Signal,
					"warning":     opt.Warning,
					"feedback":    fmt.Sprintf("You chose: %s. Signal: %s", opt.Text, opt.Signal),
				})
				break
			}
		}
		if proficiency == 0 {
			proficiency = 1 // Default if no option matched
			aiEvalJSON = json.RawMessage(`{"proficiency": 1, "feedback": "No matching option found"}`)
		}

	case "budget_allocation":
		// Evaluate budget allocation
		proficiency = s.evaluateBudgetAllocation(respMap)
		aiEvalJSON, _ = json.Marshal(map[string]interface{}{
			"proficiency": proficiency,
			"feedback":    "Budget allocation evaluated.",
		})

	default:
		proficiency = 2
		aiEvalJSON = json.RawMessage(`{"proficiency": 2, "feedback": "Response recorded."}`)
	}

	// 5. Save response
	competenciesJSON, _ := json.Marshal(question.Assess)
	stageWeights := s.DataManager.GetStageWeights(stageID)
	relevantWeights := map[string]int{}
	for _, code := range question.Assess {
		if w, ok := stageWeights[code]; ok {
			relevantWeights[code] = w
		}
	}
	weightsJSON, _ := json.Marshal(relevantWeights)

	now := time.Now()
	response := &models.Response{
		ID:                   uuid.New().String(),
		AssessmentID:         assessmentID,
		StageID:              stageRecord.ID,
		QuestionID:           questionID,
		QuestionType:         question.Type,
		ResponseData:         responseData,
		ProficiencyScore:     &proficiency,
		CompetenciesAssessed: competenciesJSON,
		StageWeights:         weightsJSON,
		AIEvaluation:         aiEvalJSON,
		StartedAt:            now,
		AnsweredAt:           &now,
	}
	if err := db.DB.Create(response).Error; err != nil {
		return nil, fmt.Errorf("failed to save response: %w", err)
	}

	// 6. Update competency scores
	s.updateCompetencyScores(assessmentID, stageID, question.Assess, proficiency)

	// 7. Update stage record
	stageRecord.QuestionsAns++
	db.DB.Save(&stageRecord)

	// 8. Determine next question or stage transition
	result := &SubmitResponseResult{
		ResponseID:   response.ID,
		AIEvaluation: aiEvalJSON,
		Proficiency:  proficiency,
	}

	nextQID := s.DataManager.GetNextQuestionID(questionID, selectedOptionID)

	if nextQID != "" {
		// More questions in this stage
		nextQ := s.DataManager.GetQuestion(nextQID)
		nextQData, _ := json.Marshal(nextQ)
		result.NextQuestion = &NextQuestionInfo{
			QID:  nextQID,
			Data: nextQData,
		}
		// Update assessment's current question
		assessment.CurrentQuestionID = nextQID
	} else {
		// Stage is complete
		result.StageCompleted = true
		stageNow := time.Now()
		stageRecord.CompletedAt = &stageNow
		db.DB.Save(&stageRecord)

		// Get next stage
		nextStageID := s.DataManager.GetNextStageID(stageID)
		if nextStageID != "" {
			nextStage := s.DataManager.GetStage(nextStageID)
			firstQ := s.DataManager.GetFirstQuestionInStage(nextStageID)
			result.NextStage = &NextStageInfo{
				ID:            nextStageID,
				Name:          nextStage.Name,
				Title:         nextStage.Title,
				StageNumber:   nextStage.StageNumber,
				Competencies:  nextStage.Competencies,
				FirstQuestion: firstQ,
			}
			assessment.CurrentStage = nextStageID
			assessment.CurrentQuestionID = firstQ
			if len(nextStage.SimulatedMonths) > 0 {
				assessment.SimulatedMonth = nextStage.SimulatedMonths[0]
			}
		} else {
			// Simulation completed
			result.SimCompleted = true
			assessment.Status = "COMPLETED"
			completedAt := time.Now()
			assessment.CompletedAt = &completedAt
		}
	}

	assessment.LastActiveAt = &now
	db.DB.Save(&assessment)

	return result, nil
}

// evaluateBudgetAllocation scores a budget allocation response
func (s *AssessmentService) evaluateBudgetAllocation(respMap map[string]interface{}) int {
	// Extract allocation percentages
	allocations, ok := respMap["allocations"].(map[string]interface{})
	if !ok {
		return 1
	}

	productDev := getFloat(allocations, "product_dev")
	marketing := getFloat(allocations, "marketing")
	hiring := getFloat(allocations, "hiring")
	buffer := getFloat(allocations, "buffer")

	warnings := 0
	if hiring > 30 {
		warnings++
	}
	if buffer < 10 {
		warnings++
	}
	if productDev < 25 {
		warnings++
	}
	if marketing > 50 {
		warnings++
	}
	_ = marketing // avoid unused warning

	switch {
	case warnings == 0:
		return 3
	case warnings == 1:
		return 2
	default:
		return 1
	}
}

func getFloat(m map[string]interface{}, key string) float64 {
	if v, ok := m[key].(float64); ok {
		return v
	}
	return 0
}

// updateCompetencyScores updates the running competency scores for an assessment
func (s *AssessmentService) updateCompetencyScores(assessmentID string, stageID string, competencies []string, proficiency int) {
	for _, code := range competencies {
		var cs models.CompetencyScore
		if err := db.DB.Where("assessmentId = ? AND competencyCode = ?", assessmentID, code).First(&cs).Error; err != nil {
			continue
		}

		// Parse existing stage scores
		stageScores := map[string][]int{}
		json.Unmarshal(cs.StageScores, &stageScores)

		// Append this score
		stageScores[stageID] = append(stageScores[stageID], proficiency)
		cs.StageScores, _ = json.Marshal(stageScores)

		// Recalculate weighted average using scoring engine
		stageCompScores := map[string]map[string][]int{} // stage -> comp -> scores
		for stage, scores := range stageScores {
			if _, ok := stageCompScores[stage]; !ok {
				stageCompScores[stage] = map[string][]int{}
			}
			stageCompScores[stage][code] = scores
		}
		results := s.ScoringEngine.CalculateCompetencyScores(stageCompScores)
		if r, ok := results[code]; ok {
			cs.WeightedAverage = r.WeightedAverage
			cs.Category = r.Category
		}

		db.DB.Save(&cs)
	}
}

// ============================================
// GET ASSESSMENT
// ============================================

type AssessmentState struct {
	Assessment            *models.Assessment `json:"assessment"`
	CurrentStageQuestions json.RawMessage    `json:"currentStageQuestions"`
	CurrentStage          json.RawMessage    `json:"currentStage"`
	Progress              json.RawMessage    `json:"progress"`
	Competencies          json.RawMessage    `json:"competencies"`
}

func (s *AssessmentService) GetAssessment(assessmentID string) (*AssessmentState, error) {
	var assessment models.Assessment
	if err := db.DB.Where("id = ?", assessmentID).First(&assessment).Error; err != nil {
		return nil, errors.New("assessment not found")
	}

	// Get current stage questions data
	var currentQ json.RawMessage
	stage := s.DataManager.GetStage(assessment.CurrentStage)
	if stage != nil && len(stage.Questions) > 0 {
		currentQ, _ = json.Marshal(stage.Questions)
	} else {
		currentQ = json.RawMessage(`[]`)
	}

	// Get current stage data
	var currentS json.RawMessage
	if stage != nil {
		currentS, _ = json.Marshal(stage)
	}

	// Calculate progress
	totalQ := len(s.DataManager.QuestionMap)
	var answeredQ int64
	db.DB.Model(&models.Response{}).Where("assessmentId = ?", assessmentID).Count(&answeredQ)
	progress, _ := json.Marshal(map[string]interface{}{
		"totalQuestions":           totalQ,
		"answeredQuestions":        answeredQ,
		"percentComplete":          float64(answeredQ) / float64(totalQ) * 100,
		"currentStage":             assessment.CurrentStage,
		"simulatedMonth":           assessment.SimulatedMonth,
		"mentorLifelinesRemaining": assessment.MentorLifelinesRemaining,
	})

	// Get competency scores
	var compScores []models.CompetencyScore
	db.DB.Where("assessmentId = ?", assessmentID).Find(&compScores)
	compJSON, _ := json.Marshal(compScores)

	return &AssessmentState{
		Assessment:            &assessment,
		CurrentStageQuestions: currentQ,
		CurrentStage:          currentS,
		Progress:              progress,
		Competencies:          compJSON,
	}, nil
}

// ============================================
// SUBMIT STAGE RESPONSES
// ============================================

type SubmitStageResult struct {
	AIEvaluation   json.RawMessage `json:"aiEvaluation"`
	Proficiency    int             `json:"proficiency"`
	StageCompleted bool            `json:"stageCompleted"`
	NextStage      *NextStageInfo  `json:"nextStage,omitempty"`
	SimCompleted   bool            `json:"simCompleted"`
	StateUpdates   json.RawMessage `json:"stateUpdates,omitempty"`
}

func (s *AssessmentService) SubmitStageResponses(assessmentID string, responses map[string]json.RawMessage) (*SubmitStageResult, error) {
	// 1. Verify assessment exists and is in progress
	var assessment models.Assessment
	if err := db.DB.Where("id = ?", assessmentID).First(&assessment).Error; err != nil {
		return nil, errors.New("assessment not found")
	}
	if assessment.Status != "IN_PROGRESS" {
		return nil, errors.New("assessment is not in progress")
	}

	stageID := assessment.CurrentStage
	stageData := s.DataManager.GetStage(stageID)
	if stageData == nil {
		return nil, errors.New("invalid stage ID")
	}

	// 2. Get or create stage record
	var stageRecord models.Stage
	if err := db.DB.Where("assessmentId = ? AND stageName = ?", assessmentID, stageID).First(&stageRecord).Error; err != nil {
		now := time.Now()
		stageRecord = models.Stage{
			ID:           uuid.New().String(),
			AssessmentID: assessmentID,
			StageName:    stageID,
			StageNumber:  stageData.StageNumber,
			StartedAt:    &now,
		}
		db.DB.Create(&stageRecord)
	}

	// 3. Process each response
	totalProficiency := 0
	questionsAnswered := 0
	stageWeights := s.DataManager.GetStageWeights(stageID)

	competenciesToUpdate := make(map[string]bool)

	for qID, respData := range responses {
		question := s.DataManager.GetQuestion(qID)
		if question == nil {
			continue // Skip invalid questions
		}

		var proficiency int
		var aiEvalJSON json.RawMessage

		// Parse response data
		var respMap map[string]interface{}
		json.Unmarshal(respData, &respMap)

		selectedOptionID := ""
		responseText := ""
		if val, ok := respMap["selectedOptionId"].(string); ok {
			selectedOptionID = val
		}
		if val, ok := respMap["text"].(string); ok {
			responseText = val
		}

		switch question.Type {
		case "open_text":
			compDefs := s.DataManager.GetCompetencyDefs()
			eval, err := s.AIService.EvaluateOpenText(
				question.Text,
				responseText,
				question.Assess,
				compDefs,
			)
			if err != nil {
				eval = &TextEvaluation{Proficiency: 2, Feedback: "Response recorded."}
			}
			proficiency = eval.Proficiency
			aiEvalJSON, _ = json.Marshal(eval)

		case "multiple_choice", "scenario":
			for _, opt := range question.Options {
				if opt.ID == selectedOptionID {
					proficiency = opt.Proficiency
					aiEvalJSON, _ = json.Marshal(map[string]interface{}{
						"proficiency": proficiency,
						"signal":      opt.Signal,
						"warning":     opt.Warning,
						"feedback":    fmt.Sprintf("You chose: %s. Signal: %s", opt.Text, opt.Signal),
					})
					break
				}
			}
			if proficiency == 0 {
				proficiency = 1
				aiEvalJSON = json.RawMessage(`{"proficiency": 1, "feedback": "No matching option found"}`)
			}

		case "budget_allocation":
			proficiency = s.evaluateBudgetAllocation(respMap)
			aiEvalJSON, _ = json.Marshal(map[string]interface{}{
				"proficiency": proficiency,
				"feedback":    "Budget allocation evaluated.",
			})

		default:
			proficiency = 2
			aiEvalJSON = json.RawMessage(`{"proficiency": 2, "feedback": "Response recorded."}`)
		}

		competenciesJSON, _ := json.Marshal(question.Assess)
		relevantWeights := map[string]int{}
		for _, code := range question.Assess {
			if w, ok := stageWeights[code]; ok {
				relevantWeights[code] = w
			}
			competenciesToUpdate[code] = true
		}
		weightsJSON, _ := json.Marshal(relevantWeights)

		now := time.Now()
		response := &models.Response{
			ID:                   uuid.New().String(),
			AssessmentID:         assessmentID,
			StageID:              stageRecord.ID,
			QuestionID:           qID,
			QuestionType:         question.Type,
			ResponseData:         respData,
			ProficiencyScore:     &proficiency,
			CompetenciesAssessed: competenciesJSON,
			StageWeights:         weightsJSON,
			AIEvaluation:         aiEvalJSON,
			StartedAt:            now,
			AnsweredAt:           &now,
		}
		db.DB.Create(response)

		totalProficiency += proficiency
		questionsAnswered++
	}

	// 4. Update stage record
	stageRecord.QuestionsAns += questionsAnswered
	stageNow := time.Now()
	stageRecord.CompletedAt = &stageNow
	db.DB.Save(&stageRecord)

	// Calculate average proficiency
	avgProficiency := 2
	if questionsAnswered > 0 {
		avgFloat := float64(totalProficiency) / float64(questionsAnswered)
		if avgFloat >= 2.5 {
			avgProficiency = 3
		} else if avgFloat >= 1.5 {
			avgProficiency = 2
		} else {
			avgProficiency = 1
		}
	}

	// 5. Update competency scores
	var compArray []string
	for code := range competenciesToUpdate {
		compArray = append(compArray, code)
	}
	s.updateCompetencyScores(assessmentID, stageID, compArray, avgProficiency)

	// 6. Generate stage feedback
	stageEvalJSON, _ := json.Marshal(map[string]interface{}{
		"feedback": fmt.Sprintf("Stage '%s' completed successfully. Average proficiency: %d", stageData.Title, avgProficiency),
	})

	result := &SubmitStageResult{
		AIEvaluation:   stageEvalJSON,
		Proficiency:    avgProficiency,
		StageCompleted: true,
	}

	// 7. Determine next stage transition
	nextStageID := s.DataManager.GetNextStageID(stageID)
	if nextStageID != "" {
		nextStage := s.DataManager.GetStage(nextStageID)
		if nextStage != nil {
			firstQ := s.DataManager.GetFirstQuestionInStage(nextStageID)
			result.NextStage = &NextStageInfo{
				ID:            nextStageID,
				Name:          nextStage.Name,
				Title:         nextStage.Title,
				StageNumber:   nextStage.StageNumber,
				Competencies:  nextStage.Competencies,
				FirstQuestion: firstQ,
			}
			assessment.CurrentStage = nextStageID
			assessment.CurrentQuestionID = firstQ
			if len(nextStage.SimulatedMonths) > 0 {
				assessment.SimulatedMonth = nextStage.SimulatedMonths[0]
			}
		}
	} else {
		result.SimCompleted = true
		assessment.Status = "COMPLETED"
		completedAt := time.Now()
		assessment.CompletedAt = &completedAt
	}

	now := time.Now()
	assessment.LastActiveAt = &now
	db.DB.Save(&assessment)

	return result, nil
}

// ============================================
// USE MENTOR LIFELINE
// ============================================

type MentorLifelineResult struct {
	MentorID      string `json:"mentorId"`
	MentorName    string `json:"mentorName"`
	Guidance      string `json:"guidance"`
	LifelinesLeft int    `json:"lifelinesLeft"`
}

func (s *AssessmentService) UseMentorLifeline(assessmentID string, mentorID string, userQuestion string) (*MentorLifelineResult, error) {
	var assessment models.Assessment
	if err := db.DB.Where("id = ?", assessmentID).First(&assessment).Error; err != nil {
		return nil, errors.New("assessment not found")
	}

	if assessment.Status != "IN_PROGRESS" {
		return nil, errors.New("assessment is not in progress")
	}

	if assessment.MentorLifelinesRemaining <= 0 {
		return nil, errors.New("no mentor lifelines remaining")
	}

	mentor, ok := s.DataManager.MentorMap[mentorID]
	if !ok {
		return nil, errors.New("invalid mentor ID")
	}

	// Get current question for context
	questionContext := "General business guidance"
	// Only include specific question context for non-ideation phases
	if assessment.CurrentStage != "STAGE_NEG2_IDEATION" {
		if q := s.DataManager.GetQuestion(assessment.CurrentQuestionID); q != nil {
			questionContext = q.Text
		}
	}

	// Generate mentor guidance via AI
	guidance, err := s.AIService.GenerateMentorGuidance(
		mentor.Name,
		mentor.GuidanceStyle,
		mentor.Tone,
		questionContext,
		assessment.UserIdea,
		userQuestion,
	)
	if err != nil {
		guidance = fmt.Sprintf("As %s, I'd suggest: Think carefully about the long-term implications here.", mentor.Name)
	}

	// Save mentor interaction
	now := time.Now()
	interactionContext := questionContext
	if userQuestion != "" {
		interactionContext = fmt.Sprintf("Context: %s | User asked: %s", questionContext, userQuestion)
	}

	interaction := &models.MentorInteraction{
		ID:              uuid.New().String(),
		AssessmentID:    assessmentID,
		MentorID:        mentorID,
		MentorName:      mentor.Name,
		StageName:       assessment.CurrentStage,
		QuestionContext: interactionContext,
		GuidanceGiven:   guidance,
		UsedAt:          now,
	}
	db.DB.Create(interaction)

	// Decrement lifelines
	assessment.MentorLifelinesRemaining--
	db.DB.Save(&assessment)

	return &MentorLifelineResult{
		MentorID:      mentorID,
		MentorName:    mentor.Name,
		Guidance:      guidance,
		LifelinesLeft: assessment.MentorLifelinesRemaining,
	}, nil
}

// ============================================
// WAR ROOM - SUBMIT PITCH
// ============================================

func (s *AssessmentService) SubmitPitch(assessmentID string, pitchText string) (map[string]interface{}, error) {
	var assessment models.Assessment
	if err := db.DB.Where("id = ?", assessmentID).First(&assessment).Error; err != nil {
		return nil, errors.New("assessment not found")
	}

	assessment.WarRoomPitch = pitchText
	db.DB.Save(&assessment)

	// Return investor panel info
	investors := s.DataManager.GetInvestors()
	investorData := make([]map[string]interface{}, 0, len(investors))
	for _, inv := range investors {
		investorData = append(investorData, map[string]interface{}{
			"id":                inv.ID,
			"name":              inv.Name,
			"primaryLens":       inv.PrimaryLens,
			"signatureQuestion": inv.SignatureQuestion,
			"avatar":            inv.Avatar,
		})
	}

	return map[string]interface{}{
		"pitchReceived": true,
		"investors":     investorData,
		"message":       "Your pitch has been received. The investor panel is ready to question you.",
	}, nil
}

// ============================================
// WAR ROOM - RESPOND TO INVESTOR
// ============================================

func (s *AssessmentService) RespondToInvestor(assessmentID string, investorID string, response string) (*models.InvestorScorecard, error) {
	var assessment models.Assessment
	if err := db.DB.Where("id = ?", assessmentID).First(&assessment).Error; err != nil {
		return nil, errors.New("assessment not found")
	}

	investor, ok := s.DataManager.InvestorMap[investorID]
	if !ok {
		return nil, errors.New("invalid investor ID")
	}

	// AI evaluates the investor response
	eval, err := s.AIService.EvaluateInvestorResponse(
		investor.Name,
		investor.PrimaryLens,
		investor.BiasTraitName,
		investor.SignatureQuestion,
		response,
	)
	if err != nil {
		eval = &InvestorEvaluation{
			PrimaryScore:   3,
			BiasTraitScore: 3,
			RedFlags:       []string{},
			Reaction:       "I need to think about this.",
		}
	}

	// Calculate deal decision
	hasRedFlag := len(eval.RedFlags) > 0
	redFlagJSON, _ := json.Marshal(eval.RedFlags)

	// Get capital ask from War Room prep responses
	capitalAsked := 100000.0 // default
	equityOffered := 10.0    // default

	deal := CalculateDealDecision(eval.PrimaryScore, eval.BiasTraitScore, hasRedFlag, capitalAsked, equityOffered)
	dealProposedJSON, _ := json.Marshal(deal)

	scorecard := &models.InvestorScorecard{
		ID:               uuid.New().String(),
		AssessmentID:     assessmentID,
		InvestorID:       investorID,
		InvestorName:     investor.Name,
		PrimaryScore:     &eval.PrimaryScore,
		BiasTraitScore:   &eval.BiasTraitScore,
		BiasTraitName:    investor.BiasTraitName,
		RedFlag:          hasRedFlag,
		RedFlagReasons:   redFlagJSON,
		DealDecision:     deal.Decision,
		DealProposed:     dealProposedJSON,
		Question:         investor.SignatureQuestion,
		ParticipantResp:  response,
		InvestorReaction: eval.Reaction,
	}

	db.DB.Create(scorecard)

	return scorecard, nil
}

// ============================================
// GENERATE REPORT
// ============================================

func (s *AssessmentService) GenerateReport(assessmentID string) (*models.Report, error) {
	var assessment models.Assessment
	if err := db.DB.Where("id = ?", assessmentID).First(&assessment).Error; err != nil {
		return nil, errors.New("assessment not found")
	}

	// Get all competency scores
	var compScores []models.CompetencyScore
	db.DB.Where("assessmentId = ?", assessmentID).Find(&compScores)

	// Build competency results map
	compResults := make(map[string]*CompetencyResult)
	for _, cs := range compScores {
		compResults[cs.CompetencyCode] = &CompetencyResult{
			Code:            cs.CompetencyCode,
			Name:            cs.CompetencyName,
			WeightedAverage: cs.WeightedAverage,
			Category:        cs.Category,
		}
	}

	// Rank competencies
	ranked := RankCompetencies(compResults)

	// Competency ranking JSON
	rankingData := make([]map[string]interface{}, 0, len(ranked))
	spiderData := make(map[string]float64)
	for i, r := range ranked {
		rankingData = append(rankingData, map[string]interface{}{
			"rank":            i + 1,
			"code":            r.Code,
			"name":            r.Name,
			"weightedAverage": r.WeightedAverage,
			"category":        r.Category,
		})
		spiderData[r.Code] = r.WeightedAverage
	}
	rankingJSON, _ := json.Marshal(rankingData)
	spiderJSON, _ := json.Marshal(spiderData)

	// Entrepreneur type
	entProfile := ClassifyEntrepreneur(compResults)

	// Role fit
	roleFit := DetermineRoleFit(ranked)
	roleFitJSON, _ := json.Marshal(roleFit)

	// Generate archetype narrative
	archetypeNarrative, _ := s.AIService.GenerateArchetypeNarrative(
		rankingData,
		nil, // stage decisions (can be populated later)
		entProfile.Type,
		roleFit.Role,
	)

	// Action plan
	actionPlan := s.generateActionPlan(ranked)
	actionPlanJSON, _ := json.Marshal(actionPlan)

	// Stage narrations
	var stages []models.Stage
	db.DB.Where("assessmentId = ?", assessmentID).Order("stageNumber ASC").Find(&stages)

	stageNarrations := make([]map[string]interface{}, 0, len(stages))
	for _, st := range stages {
		stageNarrations = append(stageNarrations, map[string]interface{}{
			"stage":             st.StageName,
			"stageNumber":       st.StageNumber,
			"questionsAnswered": st.QuestionsAns,
		})
	}
	stageNarrationsJSON, _ := json.Marshal(stageNarrations)

	// Deal summary
	var scorecards []models.InvestorScorecard
	db.DB.Where("assessmentId = ?", assessmentID).Find(&scorecards)

	dealSummary := map[string]interface{}{
		"totalInvestors":  len(scorecards),
		"dealsOffered":    0,
		"bestDeal":        nil,
		"investorResults": scorecards,
	}
	dealsOffered := 0
	for _, sc := range scorecards {
		if sc.DealDecision != "WALK_OUT" {
			dealsOffered++
		}
	}
	dealSummary["dealsOffered"] = dealsOffered
	dealSummaryJSON, _ := json.Marshal(dealSummary)

	// ---- Collect ALL user responses with question text ----
	var allResponses []models.Response
	db.DB.Where("assessmentId = ?", assessmentID).Order("startedAt ASC").Find(&allResponses)

	// Build stage ID -> stage name map
	stageIDToName := make(map[string]string)
	for _, st := range stages {
		stageIDToName[st.ID] = st.StageName
	}

	type UserResponseEntry struct {
		StageName    string          `json:"stageName"`
		QuestionID   string          `json:"questionId"`
		QuestionText string          `json:"questionText"`
		QuestionType string          `json:"questionType"`
		Response     json.RawMessage `json:"response"`
		Proficiency  *int            `json:"proficiency"`
		AIFeedback   json.RawMessage `json:"aiFeedback"`
	}

	userResponseEntries := make([]UserResponseEntry, 0, len(allResponses))
	var responseSummaryLines []string

	for _, r := range allResponses {
		stageName := stageIDToName[r.StageID]
		questionText := r.QuestionID
		q := s.DataManager.GetQuestion(r.QuestionID)
		if q != nil {
			questionText = q.Text
		}

		entry := UserResponseEntry{
			StageName:    stageName,
			QuestionID:   r.QuestionID,
			QuestionText: questionText,
			QuestionType: r.QuestionType,
			Response:     r.ResponseData,
			Proficiency:  r.ProficiencyScore,
			AIFeedback:   r.AIEvaluation,
		}
		userResponseEntries = append(userResponseEntries, entry)

		// Build summary for AI
		var respMap map[string]interface{}
		json.Unmarshal(r.ResponseData, &respMap)
		respText := ""
		switch r.QuestionType {
		case "multiple_choice", "scenario":
			if optID, ok := respMap["selectedOptionId"].(string); ok && q != nil {
				for _, opt := range q.Options {
					if opt.ID == optID {
						respText = opt.Text
						break
					}
				}
			}
		case "open_text":
			if text, ok := respMap["text"].(string); ok {
				if len(text) > 150 {
					text = text[:150] + "..."
				}
				respText = text
			}
		}
		profStr := ""
		if r.ProficiencyScore != nil {
			profStr = fmt.Sprintf(" (P%d)", *r.ProficiencyScore)
		}
		responseSummaryLines = append(responseSummaryLines, fmt.Sprintf("[%s] %s → %s%s", stageName, questionText, respText, profStr))
	}

	userResponsesJSON, _ := json.Marshal(userResponseEntries)

	// Build investor summary for AI
	var investorSummaryLines []string
	for _, sc := range scorecards {
		primaryScore := 0
		if sc.PrimaryScore != nil {
			primaryScore = *sc.PrimaryScore
		}
		investorSummaryLines = append(investorSummaryLines, fmt.Sprintf("- %s: Question=\"%s\" | Response=\"%s\" | Reaction=\"%s\" | Score=%d/5 | Decision=%s",
			sc.InvestorName, sc.Question, sc.ParticipantResp, sc.InvestorReaction, primaryScore, sc.DealDecision))
	}

	// Generate detailed AI analysis
	responseSummary := strings.Join(responseSummaryLines, "\n")
	if len(responseSummary) > 4000 {
		responseSummary = responseSummary[:4000] + "\n... (truncated)"
	}
	investorSummary := strings.Join(investorSummaryLines, "\n")
	if investorSummary == "" {
		investorSummary = "No investor interactions recorded."
	}

	detailedAnalysis, err := s.AIService.GenerateDetailedAnalysis(
		rankingData,
		responseSummary,
		investorSummary,
		entProfile.Type,
		roleFit.Role,
		assessment.UserIdea,
	)
	if err != nil {
		log.Printf("[Report] Detailed analysis generation error: %v", err)
		detailedAnalysis = "Detailed analysis could not be generated."
	}

	// Create report
	report := &models.Report{
		ID:                 uuid.New().String(),
		AssessmentID:       assessmentID,
		ReportType:         "FINAL",
		DealSummary:        dealSummaryJSON,
		CompetencyRanking:  rankingJSON,
		SpiderChartData:    spiderJSON,
		ArchetypeNarrative: archetypeNarrative,
		EntrepreneurType:   entProfile.Type,
		OrganizationalRole: roleFit.Role,
		ActionPlan:         actionPlanJSON,
		StageNarrations:    stageNarrationsJSON,
		RoleFitMap:         roleFitJSON,
		DetailedAnalysis:   detailedAnalysis,
		UserResponses:      userResponsesJSON,
		GeneratedAt:        time.Now(),
	}

	db.DB.Create(report)

	log.Printf("[Report] Generated for assessment %s: type=%s, role=%s", assessmentID, entProfile.Type, roleFit.Role)
	return report, nil
}

// generateActionPlan creates 3 action items based on weakest competencies
func (s *AssessmentService) generateActionPlan(ranked []*CompetencyResult) []map[string]interface{} {
	actions := make([]map[string]interface{}, 0)

	// Focus on bottom 3 competencies
	for i := len(ranked) - 1; i >= 0 && len(actions) < 3; i-- {
		r := ranked[i]
		if r.WeightedAverage < 2.7 { // Anything below Natural Dominant needs work
			actions = append(actions, map[string]interface{}{
				"competency": r.Code + " - " + r.Name,
				"category":   r.Category,
				"currentAvg": r.WeightedAverage,
				"action":     fmt.Sprintf("Focus on developing %s through deliberate practice and reflection.", r.Name),
				"targetDate": "Next 90 days",
			})
		}
	}

	return actions
}

// ============================================
// LIST ASSESSMENTS
// ============================================

func (s *AssessmentService) ListAssessments(userID string) ([]models.Assessment, error) {
	var assessments []models.Assessment
	if err := db.DB.Where("userId = ?", userID).Order("createdAt DESC").Find(&assessments).Error; err != nil {
		return nil, err
	}
	return assessments, nil
}

// ============================================
// GET SCORECARDS FOR ASSESSMENT
// ============================================

func (s *AssessmentService) GetScorecards(assessmentID string) ([]models.InvestorScorecard, error) {
	var scorecards []models.InvestorScorecard
	if err := db.DB.Where("assessmentId = ?", assessmentID).Find(&scorecards).Error; err != nil {
		return nil, err
	}
	return scorecards, nil
}
