package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"war-room-backend/internal/broadcast"
	"war-room-backend/internal/db"
	"war-room-backend/internal/models"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// ============================================
// ASSESSMENT SERVICE - SOP 2.0
// ============================================

type AssessmentService struct {
	DataManager          *DataManager
	AIService            *AIService
	TTSService           *TTSService
	ScoringEngine        *ScoringEngine
	dynamicScenarioLocks sync.Map
}

func NewAssessmentService(dm *DataManager) *AssessmentService {
	ai := NewAIService()
	tts := NewTTSService()
	se := NewScoringEngine(dm.Config)
	return &AssessmentService{
		DataManager:   dm,
		AIService:     ai,
		TTSService:    tts,
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
		RevenueProjection:        0,
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
			Category:        "P1",
		}
		db.DB.Create(cs)
	}

	log.Printf("[Assessment] Created: id=%s, user=%s, level=%d, stage=%s", assessment.ID, userID, req.Level, firstStage)

	// Pre-generate dynamic scenarios for the first stage in a goroutine
	go s.PreGenerateDynamicScenarios(assessment.ID, firstStage)

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

	case "dynamic_scenario":
		// These are handled via SubmitDynamicScenario normally, but if submitted here, score it
		if proficiencyVal, ok := respMap["proficiency"].(float64); ok {
			proficiency = int(proficiencyVal)
		} else {
			proficiency = 2
		}
		aiEvalJSON, _ = json.Marshal(map[string]interface{}{
			"proficiency": proficiency,
			"feedback":    "Scenario decision recorded.",
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

	// Store previous responses for AI context
	s.updatePreviousResponses(assessmentID, question.Text, responseData, proficiency)

	// 6. Update simulation state based on specific question types
	if questionID == "Q_0_1" {
		// Capital Generation
		for _, opt := range question.Options {
			if opt.ID == selectedOptionID {
				assessment.Capital = 50000
				assessment.CapitalSource = opt.Text
				// Apply impact to FinancialState
				var finState map[string]interface{}
				json.Unmarshal(assessment.FinancialState, &finState)
				if opt.Impact != nil {
					for k, v := range opt.Impact {
						finState[k] = v
					}
				}
				finState["capital"] = 50000
				assessment.FinancialState, _ = json.Marshal(finState)
				break
			}
		}
	} else if questionID == "Q_0_3" {
		// Budget Allocation
		if allocations, ok := respMap["allocations"]; ok {
			assessment.BudgetAllocations, _ = json.Marshal(allocations)
			// Update FinancialState with burn rate based on allocations
			var finState map[string]interface{}
			json.Unmarshal(assessment.FinancialState, &finState)
			// Simple logic: higher hiring/marketing = higher burn
			allocMap, _ := allocations.(map[string]interface{})
			burn := 0.0
			for k, v := range allocMap {
				val := getFloat(allocMap, k)
				if k == "hiring" || k == "operations" {
					burn += val * 100 // Example: % of capital becomes monthly burn
				}
				_ = v
			}
			finState["burnRate"] = burn
			if burn > 0 {
				finState["runway"] = assessment.Capital / burn
			}
			assessment.FinancialState, _ = json.Marshal(finState)
		}
	}

	// 7. Update competency scores
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

	// Special flow handling
	if selectedOptionID == "Q_0_DECISION_RESTART" {
		assessment.RestartCount++
		nextQID = s.DataManager.GetFirstQuestionInStage("STAGE_NEG2_IDEATION")
		assessment.CurrentStage = "STAGE_NEG2_IDEATION"
		assessment.CurrentQuestionID = nextQID
		assessment.SimulatedMonth = 0
		db.DB.Save(&assessment)

		nextStage := s.DataManager.GetStage("STAGE_NEG2_IDEATION")
		result.NextStage = &NextStageInfo{
			ID:            "STAGE_NEG2_IDEATION",
			Name:          nextStage.Name,
			Title:         nextStage.Title,
			StageNumber:   nextStage.StageNumber,
			Competencies:  nextStage.Competencies,
			FirstQuestion: nextQID,
		}
		result.StageCompleted = true
		return result, nil
	}

	if selectedOptionID == "Q_3_DECISION_BUYOUT" {
		assessment.BuyoutChosen = true
		assessment.Status = "COMPLETED"
		completedAt := time.Now()
		assessment.CompletedAt = &completedAt
		db.DB.Save(&assessment)

		result.SimCompleted = true
		go s.GenerateReport(assessmentID)
		return result, nil
	}

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
			// Pre-generate dynamic scenarios for the new stage in a goroutine
			go s.PreGenerateDynamicScenarios(assessmentID, nextStageID)
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

// ============================================
// DYNAMIC SCENARIO HANDLING
// ============================================

func (s *AssessmentService) PreGenerateDynamicScenarios(assessmentID string, stageID string) {
	stage := s.DataManager.GetStage(stageID)
	if stage == nil {
		return
	}

	for _, q := range stage.Questions {
		if q.Type == "dynamic_scenario" {
			// Trigger generation for each dynamic question
			// We call GetDynamicScenario which handles caching and generation
			log.Printf("[PreGenerate] Generating dynamic scenario for assessment %s, stage %s, question %s", assessmentID, stageID, q.QID)
			_, err := s.GetDynamicScenario(assessmentID, stageID, q.QID)
			if err != nil {
				log.Printf("[PreGenerate] Error generating for %s: %v", q.QID, err)
			}
		}
	}
}

func (s *AssessmentService) GetDynamicScenario(assessmentID string, stageID string, questionID string) (*models.DynamicScenario, error) {
	lockKey := fmt.Sprintf("%s:%s:%s", assessmentID, stageID, questionID)
	lockValue, _ := s.dynamicScenarioLocks.LoadOrStore(lockKey, &sync.Mutex{})
	lock := lockValue.(*sync.Mutex)
	lock.Lock()
	defer lock.Unlock()

	// 1. Check if exists
	var scenario models.DynamicScenario
	err := db.DB.Where("assessment_id = ? AND stage_id = ? AND question_id = ?", assessmentID, stageID, questionID).First(&scenario).Error
	if err == nil {
		if !isLegacyFallbackDynamicScenario(&scenario) {
			sanitized := sanitizeDynamicScenarioQuestionText(scenario.QuestionText)
			if sanitized != scenario.QuestionText {
				scenario.QuestionText = sanitized
				db.DB.Model(&models.DynamicScenario{}).Where("id = ?", scenario.ID).Update("question_text", sanitized)
			}
			return &scenario, nil
		}
		log.Printf("[Assessment] Legacy fallback scenario detected for %s, regenerating with Gemini", questionID)
		if delErr := db.DB.Delete(&scenario).Error; delErr != nil {
			return nil, fmt.Errorf("failed to replace legacy scenario: %w", delErr)
		}
	}

	// 2. Generate new
	var assessment models.Assessment
	db.DB.First(&assessment, "id = ?", assessmentID)

	stage := s.DataManager.GetStage(stageID)
	if stage == nil {
		return nil, fmt.Errorf("stage not found: %s", stageID)
	}

	question := s.DataManager.GetQuestion(questionID)
	if question == nil {
		return nil, fmt.Errorf("question not found: %s", questionID)
	}

	compDefs := s.DataManager.GetCompetencyDefs()
	previousResponsesStr := string(assessment.PreviousResponses)

	questionContext := questionID
	if strings.TrimSpace(question.Tag) != "" {
		questionContext = fmt.Sprintf("%s (%s)", question.Tag, questionID)
	} else if strings.TrimSpace(question.Text) != "" {
		questionContext = fmt.Sprintf("%s (%s)", question.Text, questionID)
	}

	// Determine which leader will ask this scenario
	var selectedLeaders []string
	json.Unmarshal(assessment.SelectedLeaders, &selectedLeaders)
	if len(selectedLeaders) == 0 {
		selectedLeaders = []string{"indira_nooyi", "jack_ma", "simon_sinek"}
	}

	leaderIdx := 0
	if strings.Contains(questionID, "_SCENARIO_2") {
		leaderIdx = 1
	}
	if leaderIdx >= len(selectedLeaders) {
		leaderIdx = 0
	}

	leaderID := selectedLeaders[leaderIdx]
	leader := s.DataManager.GetLeader(leaderID)
	leaderName := leaderID
	if leader != nil {
		leaderName = leader.Name
	}

	aiResp, err := s.AIService.GenerateDynamicScenario(
		questionContext,
		stage.Goal,
		stage.ResearchBackground,
		question.Assess,
		compDefs,
		previousResponsesStr,
		assessment.UserIdea,
		leaderName,
	)
	if err != nil {
		log.Printf("[Assessment] Dynamic scenario generation failed for %s, using fail-safe scenario: %v", questionID, err)
		aiResp = buildFallbackDynamicScenarioResponse(questionContext, stage.Goal, leaderName)
	}
	aiResp.Question = sanitizeDynamicScenarioQuestionText(aiResp.Question)

	// Map AI response to SimOptions
	var options []models.SimOption
	for i, opt := range aiResp.Options {
		profInt := 2
		if v, ok := opt.Proficiency.(int); ok {
			profInt = v
		}
		options = append(options, models.SimOption{
			ID:          fmt.Sprintf("%s_OPT_%d", questionID, i+1),
			Text:        opt.Text,
			Proficiency: profInt,
			Feedback:    opt.Feedback,
		})
	}

	optionsJSON, _ := json.Marshal(options)

	scenario = models.DynamicScenario{
		ID:           uuid.New().String(),
		AssessmentID: assessmentID,
		StageID:      stageID,
		QuestionID:   questionID,
		QuestionText: aiResp.Question,
		Options:      optionsJSON,
		CreatedAt:    time.Now(),
	}

	if err := db.DB.Where("assessment_id = ? AND stage_id = ? AND question_id = ?", assessmentID, stageID, questionID).
		Assign(map[string]interface{}{
			"id":            scenario.ID,
			"question_text": aiResp.Question,
			"options":       optionsJSON,
			"created_at":    scenario.CreatedAt,
			"assessment_id": assessmentID,
			"stage_id":      stageID,
			"question_id":   questionID,
		}).FirstOrCreate(&scenario).Error; err != nil {
		return nil, err
	}

	return &scenario, nil
}

func isLegacyFallbackDynamicScenario(s *models.DynamicScenario) bool {
	question := strings.TrimSpace(strings.ToLower(s.QuestionText))
	if question == "" {
		return true
	}

	if strings.Contains(question, "a sudden operational challenge has emerged") ||
		question == "dynamic scenario 1" ||
		question == "dynamic scenario 2" ||
		strings.HasPrefix(question, "because of your previous decision to prioritize speed") {
		return true
	}

	var options []models.SimOption
	if err := json.Unmarshal(s.Options, &options); err == nil && len(options) > 0 {
		first := strings.TrimSpace(strings.ToLower(options[0].Text))
		if strings.Contains(first, "address the root cause immediately with your team") || strings.Contains(first, "pause and identify root causes") {
			return true
		}
	}

	return false
}

func buildFallbackDynamicScenarioResponse(questionContext string, stageGoal string, leaderName string) *DynamicScenarioResponse {
	_ = questionContext
	prompt := fmt.Sprintf("%s reviews your recent choices and notes a developing situation: Your team now faces a critical decision regarding %s. How will you handle this?", leaderName, strings.TrimSpace(stageGoal))
	if strings.TrimSpace(stageGoal) == "" {
		prompt = fmt.Sprintf("%s reviews your recent choices and notes a developing situation: Your team now faces a critical trade-off. How will you handle this?", leaderName)
	}

	return &DynamicScenarioResponse{
		Question: prompt,
		Options: []struct {
			Text        string      `json:"text"`
			Proficiency interface{} `json:"proficiency"`
			Feedback    string      `json:"feedback"`
		}{
			{Text: "Pause and identify root causes with customer and operational data before acting.", Proficiency: 3, Feedback: "Strong decision quality through diagnosis before execution."},
			{Text: "Run a small experiment to test one focused fix this week.", Proficiency: 2, Feedback: "Balanced speed and learning, but may miss broader issues."},
			{Text: "Keep current plan unchanged and wait for the issue to settle.", Proficiency: 1, Feedback: "Low adaptability under pressure."},
			{Text: "Cut one initiative to protect runway, then reallocate effort to the highest-impact problem.", Proficiency: 2, Feedback: "Pragmatic trade-off with moderate strategic depth."},
		},
	}
}

func sanitizeDynamicScenarioQuestionText(questionText string) string {
	questionText = strings.TrimSpace(questionText)
	if !strings.HasPrefix(questionText, "[") {
		return questionText
	}

	closingBracket := strings.Index(questionText, "]")
	if closingBracket <= 0 || closingBracket >= len(questionText)-1 {
		return questionText
	}

	return strings.TrimSpace(questionText[closingBracket+1:])
}

// GetStageDynamicScenarios retrieves all pre-generated or cached dynamic scenarios for an assessment stage
func (s *AssessmentService) GetStageDynamicScenarios(assessmentID string, stageID string) ([]models.DynamicScenario, error) {
	var scenarios []models.DynamicScenario
	if err := db.DB.Where("assessment_id = ? AND stage_id = ?", assessmentID, stageID).Find(&scenarios).Error; err != nil {
		log.Printf("[Assessment] Error fetching stage dynamic scenarios: %v", err)
		return []models.DynamicScenario{}, err
	}
	for index := range scenarios {
		sanitized := sanitizeDynamicScenarioQuestionText(scenarios[index].QuestionText)
		if sanitized != scenarios[index].QuestionText {
			scenarios[index].QuestionText = sanitized
			db.DB.Model(&models.DynamicScenario{}).Where("id = ?", scenarios[index].ID).Update("question_text", sanitized)
		}
	}
	return scenarios, nil
}

func (s *AssessmentService) SubmitDynamicScenario(assessmentID string, scenarioID string, selectedOptionID string) (*SubmitResponseResult, error) {
	var scenario models.DynamicScenario
	if err := db.DB.First(&scenario, "id = ?", scenarioID).Error; err != nil {
		return nil, errors.New("scenario not found")
	}

	var options []models.SimOption
	json.Unmarshal(scenario.Options, &options)

	var selectedOpt *models.SimOption
	for _, opt := range options {
		if opt.ID == selectedOptionID {
			selectedOpt = &opt
			break
		}
	}

	if selectedOpt == nil {
		return nil, errors.New("invalid option ID")
	}

	now := time.Now()
	scenario.SelectedOptionID = selectedOptionID
	scenario.ProficiencyScore = &selectedOpt.Proficiency
	scenario.Feedback = selectedOpt.Feedback
	scenario.AnsweredAt = &now
	db.DB.Save(&scenario)

	// Submit this as a standard response so it impacts scores
	respData, _ := json.Marshal(map[string]interface{}{
		"selectedOptionId": selectedOptionID,
		"proficiency":      selectedOpt.Proficiency,
		"feedback":         selectedOpt.Feedback,
	})

	return s.SubmitResponse(assessmentID, scenario.QuestionID, respData)
}

func (s *AssessmentService) updatePreviousResponses(assessmentID string, questionText string, responseData json.RawMessage, proficiency int) {
	var assessment models.Assessment
	db.DB.First(&assessment, "id = ?", assessmentID)

	var prev []map[string]interface{}
	json.Unmarshal(assessment.PreviousResponses, &prev)

	// Parse response for human-readable text
	var respMap map[string]interface{}
	json.Unmarshal(responseData, &respMap)
	responseText := ""
	if t, ok := respMap["text"].(string); ok {
		responseText = t
	} else if optID, ok := respMap["selectedOptionId"].(string); ok {
		// Try to find the actual option text
		question := s.DataManager.GetQuestionByText(questionText)
		if question != nil {
			for _, opt := range question.Options {
				if opt.ID == optID {
					responseText = opt.Text
					break
				}
			}
		}
		if responseText == "" {
			responseText = optID
		}
	}

	prev = append(prev, map[string]interface{}{
		"q":    questionText,
		"a":    responseText,
		"prof": proficiency,
	})

	// Keep only last 10 for context window efficiency
	if len(prev) > 10 {
		prev = prev[len(prev)-10:]
	}

	assessment.PreviousResponses, _ = json.Marshal(prev)
	db.DB.Save(&assessment)
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

func extractFreeTextResponse(responseData json.RawMessage) string {
	if len(responseData) == 0 {
		return ""
	}

	var respMap map[string]interface{}
	if err := json.Unmarshal(responseData, &respMap); err != nil {
		return ""
	}

	if text, ok := respMap["text"].(string); ok {
		return strings.TrimSpace(text)
	}

	if text, ok := respMap["response"].(string); ok {
		return strings.TrimSpace(text)
	}

	return ""
}

// updateCompetencyScores updates the running competency scores for an assessment
func (s *AssessmentService) updateCompetencyScores(assessmentID string, stageID string, competencies []string, proficiency int) {
	s.BatchUpdateCompetencyScores(assessmentID, stageID, map[string][]int{stageID: {proficiency}}, competencies)
}

// BatchUpdateCompetencyScores updates competency scores in bulk for multiple stage outcomes.
func (s *AssessmentService) BatchUpdateCompetencyScores(assessmentID string, currentStageID string, stageProficiencies map[string][]int, competencies []string) {
	if len(competencies) == 0 {
		return
	}

	var competencyScores []models.CompetencyScore
	if err := db.DB.Where("assessmentId = ? AND competencyCode IN ?", assessmentID, competencies).Find(&competencyScores).Error; err != nil {
		return
	}

	competencyMap := make(map[string]*models.CompetencyScore, len(competencyScores))
	for index := range competencyScores {
		cs := &competencyScores[index]
		competencyMap[cs.CompetencyCode] = cs
	}

	for _, code := range competencies {
		cs, exists := competencyMap[code]
		if !exists {
			continue
		}

		stageScores := map[string][]int{}
		json.Unmarshal(cs.StageScores, &stageScores)

		// Add new proficiencies
		for stage, scores := range stageProficiencies {
			stageScores[stage] = append(stageScores[stage], scores...)
		}

		cs.StageScores, _ = json.Marshal(stageScores)

		// Recalculate based on ALL stages
		stageCompScores := map[string]map[string][]int{}
		for stage, scores := range stageScores {
			if _, ok := stageCompScores[stage]; !ok {
				stageCompScores[stage] = map[string][]int{}
			}
			stageCompScores[stage][code] = scores
		}

		// Fetch evidence items for this competency (from all stages)
		var responses []models.Response
		db.DB.Where("assessmentId = ?", assessmentID).Find(&responses)

		evidenceMap := make(map[string]map[string][]EvidenceItem)
		for _, resp := range responses {
			var respComps []string
			if err := json.Unmarshal(resp.CompetenciesAssessed, &respComps); err != nil {
				continue
			}

			// Find stage record to get the stage name
			var stage models.Stage
			db.DB.Where("id = ?", resp.StageID).First(&stage)

			for _, c := range respComps {
				if c != code {
					continue
				}
				if _, ok := evidenceMap[stage.StageName]; !ok {
					evidenceMap[stage.StageName] = make(map[string][]EvidenceItem)
				}
				evidenceMap[stage.StageName][c] = append(evidenceMap[stage.StageName][c], EvidenceItem{
					Stage:       stage.StageName,
					QuestionID:  resp.QuestionID,
					Proficiency: *resp.ProficiencyScore,
					AIEval:      resp.AIEvaluation,
				})
			}
		}

		results := s.ScoringEngine.CalculateCompetencyScores(stageCompScores, evidenceMap)
		if result, ok := results[code]; ok {
			cs.WeightedAverage = result.WeightedAverage
			cs.Category = result.Category
			// Save strengths and weaknesses to DB
			strJSON, _ := json.Marshal(result.Strengths)
			weakJSON, _ := json.Marshal(result.Weaknesses)
			evidJSON, _ := json.Marshal(result.Evidence)
			cs.Strengths = strJSON
			cs.Weaknesses = weakJSON
			cs.Evidence = evidJSON
		}
	}

	// Use a transaction for batch updates
	db.DB.Transaction(func(tx *gorm.DB) error {
		for _, cs := range competencyScores {
			if err := tx.Save(&cs).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// ============================================
// GET ASSESSMENT
// ============================================

type AssessmentState struct {
	Assessment            *models.Assessment `json:"assessment"`
	Simulation            *models.Assessment `json:"simulation"` // Legacy fallback for UI
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

	if strings.TrimSpace(assessment.WarRoomPitch) == "" {
		var prepPitchResponse models.Response
		err := db.DB.Where("assessmentId = ? AND questionId = ?", assessmentID, "Q_WP_1").Order("createdAt DESC").First(&prepPitchResponse).Error
		if err == nil {
			if recoveredPitch := extractFreeTextResponse(prepPitchResponse.ResponseData); recoveredPitch != "" {
				assessment.WarRoomPitch = recoveredPitch
				db.DB.Model(&models.Assessment{}).Where("id = ?", assessmentID).Update("warRoomPitch", recoveredPitch)
			}
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			log.Printf("[Assessment] failed to recover prep pitch for assessment %s: %v", assessmentID, err)
		}
	}

	// Get current stage questions data
	var currentQ json.RawMessage
	stage := s.DataManager.GetStage(assessment.CurrentStage)
	if stage != nil && len(stage.Questions) > 0 {
		currentQ, _ = json.Marshal(stage.Questions)
	} else if assessment.CurrentStage == "STAGE_NEG2_IDEATION" {
		// Fallback for Ideation stage if not in data
		currentQ = json.RawMessage(`[]`)
	} else {
		currentQ = json.RawMessage(`[]`)
	}

	// Get current stage data
	var currentS json.RawMessage
	if stage != nil {
		currentS, _ = json.Marshal(stage)
	} else if assessment.CurrentStage == "STAGE_NEG2_IDEATION" {
		currentS = json.RawMessage(`{"id":"STAGE_NEG2_IDEATION","name":"Ideation","stage_number":-2}`)
	} else {
		fallbackStage, _ := json.Marshal(map[string]interface{}{
			"id":           assessment.CurrentStage,
			"name":         "Unknown Stage",
			"title":        assessment.CurrentStage,
			"stage_number": 0,
			"questions":    []interface{}{},
		})
		currentS = fallbackStage
	}

	// Calculate progress
	totalQ := len(s.DataManager.QuestionMap)
	var answeredQ int64
	db.DB.Model(&models.Response{}).Where("assessmentId = ?", assessmentID).Count(&answeredQ)
	percentComplete := 0.0
	if totalQ > 0 {
		percentComplete = float64(answeredQ) / float64(totalQ) * 100
	}

	progress, err := json.Marshal(map[string]interface{}{
		"totalQuestions":           totalQ,
		"answeredQuestions":        answeredQ,
		"percentComplete":          percentComplete,
		"currentStage":             assessment.CurrentStage,
		"simulatedMonth":           assessment.SimulatedMonth,
		"mentorLifelinesRemaining": assessment.MentorLifelinesRemaining,
	})
	if err != nil {
		log.Printf("[Assessment] failed to marshal progress for assessment %s: %v", assessmentID, err)
		progress = json.RawMessage(`{"totalQuestions":0,"answeredQuestions":0,"percentComplete":0,"currentStage":"","simulatedMonth":0,"mentorLifelinesRemaining":0}`)
	}

	// Get competency scores
	var compScores []models.CompetencyScore
	db.DB.Where("assessmentId = ?", assessmentID).Find(&compScores)
	compJSON, _ := json.Marshal(compScores)

	return &AssessmentState{
		Assessment:            &assessment,
		Simulation:            &assessment,
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

	// 3. Prepare tasks for evaluation
	compDefs := s.DataManager.GetCompetencyDefs()
	var openTextTasks []BatchEvaluationItem
	responseResults := map[string]*PhaseResponseOut{}

	for qID, respData := range responses {
		question := s.DataManager.GetQuestion(qID)
		if question == nil {
			continue
		}

		var respMap map[string]interface{}
		json.Unmarshal(respData, &respMap)

		out := &PhaseResponseOut{
			QuestionID:   qID,
			QuestionType: question.Type,
		}

		switch question.Type {
		case "open_text":
			responseText, _ := respMap["text"].(string)
			openTextTasks = append(openTextTasks, BatchEvaluationItem{
				QuestionID:   qID,
				QuestionText: question.Text,
				ResponseText: responseText,
				Competencies: question.Assess,
			})
			out.ProficiencyScore = 0 // Placeholder

		case "multiple_choice", "scenario":
			selectedOptionID, _ := respMap["selectedOptionId"].(string)
			proficiency := 1
			feedback := "Response recorded."
			var signal, warning string

			for _, opt := range question.Options {
				if opt.ID == selectedOptionID {
					proficiency = opt.Proficiency
					signal = opt.Signal
					warning = opt.Warning
					feedback = fmt.Sprintf("You chose: %s. Signal: %s", opt.Text, opt.Signal)
					break
				}
			}
			out.ProficiencyScore = proficiency
			out.AIEvaluation, _ = json.Marshal(map[string]interface{}{
				"proficiency": proficiency,
				"signal":      signal,
				"warning":     warning,
				"feedback":    feedback,
				"source":      "predefined",
			})

		case "budget_allocation":
			proficiency := s.evaluateBudgetAllocation(respMap)
			out.ProficiencyScore = proficiency
			out.AIEvaluation, _ = json.Marshal(map[string]interface{}{
				"proficiency": proficiency,
				"feedback":    "Budget allocation evaluated.",
				"source":      "predefined",
			})

		default:
			out.ProficiencyScore = 2
			out.AIEvaluation = json.RawMessage(`{"proficiency": 2, "feedback": "Response recorded."}`)
		}

		responseResults[qID] = out
	}

	// 4. Batch AI evaluation
	if len(openTextTasks) > 0 {
		batchEvals, err := s.AIService.BatchEvaluateOpenText(openTextTasks, compDefs)
		if err == nil {
			for qID, eval := range batchEvals {
				if out, ok := responseResults[qID]; ok {
					out.ProficiencyScore = eval.Proficiency
					out.AIEvaluation, _ = json.Marshal(eval)
				}
			}
		}
	}

	// 5. Batch persist responses & collect competencies
	var dbResponses []*models.Response
	stageProficiencies := make(map[string][]int)
	allAssessedCompetencies := make(map[string]bool)
	now := time.Now()
	stageWeights := s.DataManager.GetStageWeights(stageID)
	totalProficiency := 0

	for qID, out := range responseResults {
		question := s.DataManager.GetQuestion(qID)
		if question == nil {
			continue
		}

		competenciesJSON, _ := json.Marshal(question.Assess)
		relevantWeights := map[string]int{}
		for _, code := range question.Assess {
			if w, ok := stageWeights[code]; ok {
				relevantWeights[code] = w
			}
			allAssessedCompetencies[code] = true
		}
		weightsJSON, _ := json.Marshal(relevantWeights)

		dbResponses = append(dbResponses, &models.Response{
			ID:                   uuid.New().String(),
			AssessmentID:         assessmentID,
			StageID:              stageRecord.ID,
			QuestionID:           qID,
			QuestionType:         question.Type,
			ResponseData:         responses[qID],
			ProficiencyScore:     &out.ProficiencyScore,
			CompetenciesAssessed: competenciesJSON,
			StageWeights:         weightsJSON,
			AIEvaluation:         out.AIEvaluation,
			StartedAt:            now,
			AnsweredAt:           &now,
		})
		stageProficiencies[stageID] = append(stageProficiencies[stageID], out.ProficiencyScore)
		totalProficiency += out.ProficiencyScore
	}

	if len(dbResponses) > 0 {
		db.DB.Create(&dbResponses)
	}

	// 6. Update stage record
	questionsAnswered := len(responseResults)
	stageRecord.QuestionsAns += questionsAnswered
	stageRecord.CompletedAt = &now
	db.DB.Save(&stageRecord)

	// Calculate average proficiency for current stage feedback
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

	// 7. Batch Update Competency Scores
	var compList []string
	for c := range allAssessedCompetencies {
		compList = append(compList, c)
	}
	s.BatchUpdateCompetencyScores(assessmentID, stageID, stageProficiencies, compList)

	// 8. Generate stage feedback
	stageEvalJSON, _ := json.Marshal(map[string]interface{}{
		"feedback": fmt.Sprintf("Stage '%s' completed successfully. Average proficiency: %d", stageData.Title, avgProficiency),
	})

	result := &SubmitStageResult{
		AIEvaluation:   stageEvalJSON,
		Proficiency:    avgProficiency,
		StageCompleted: true,
	}

	// 9. Determine next stage transition
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
			go s.PreGenerateDynamicScenarios(assessmentID, nextStageID)
		}
	} else {
		result.SimCompleted = true
		assessment.Status = "COMPLETED"
		assessment.CompletedAt = &now
	}

	if stageID == "STAGE_WARROOM_PREP" {
		preparedPitch := extractFreeTextResponse(responses["Q_WP_1"])
		if preparedPitch == "" {
			for qID, respData := range responses {
				question := s.DataManager.GetQuestion(qID)
				if question == nil {
					continue
				}

				isPitchTemplateQuestion := strings.EqualFold(strings.TrimSpace(question.Tag), "Pitch Template") ||
					strings.Contains(strings.ToLower(question.Text), "fill in the pitch template")
				if !isPitchTemplateQuestion {
					continue
				}

				preparedPitch = extractFreeTextResponse(respData)
				if preparedPitch != "" {
					break
				}
			}
		}

		if preparedPitch != "" {
			assessment.WarRoomPitch = preparedPitch
		}
	}

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
	var selectedIDs []string
	json.Unmarshal(assessment.SelectedInvestors, &selectedIDs)

	investors := s.DataManager.GetInvestors()
	investorData := make([]map[string]interface{}, 0, len(selectedIDs))

	idMap := make(map[string]models.Investor)
	for _, inv := range investors {
		idMap[inv.ID] = inv
	}

	for _, id := range selectedIDs {
		if inv, ok := idMap[id]; ok {
			investorData = append(investorData, map[string]interface{}{
				"id":                inv.ID,
				"name":              inv.Name,
				"primaryLens":       inv.PrimaryLens,
				"signatureQuestion": inv.SignatureQuestion,
				"avatar":            inv.Avatar,
				"bio":               inv.Bio,
			})
		}
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
// WAR ROOM - SUBMIT PITCH AUDIO
// ============================================

func (s *AssessmentService) SubmitPitchAudio(assessmentID string, audioBase64 string, mimeType string) (map[string]interface{}, error) {
	var assessment models.Assessment
	if err := db.DB.Where("id = ?", assessmentID).First(&assessment).Error; err != nil {
		return nil, errors.New("assessment not found")
	}

	// Analyze audio via Gemini
	analysis, err := s.AIService.AnalyzePitchAudio(audioBase64, mimeType)
	if err != nil {
		return nil, fmt.Errorf("failed to analyze pitch audio: %w", err)
	}

	// Save transcription as pitch text
	assessment.WarRoomPitch = analysis.Transcription
	db.DB.Save(&assessment)

	// Return investor panel info + analysis
	var selectedIDs []string
	json.Unmarshal(assessment.SelectedInvestors, &selectedIDs)

	investors := s.DataManager.GetInvestors()
	investorData := make([]map[string]interface{}, 0, len(selectedIDs))

	idMap := make(map[string]models.Investor)
	for _, inv := range investors {
		idMap[inv.ID] = inv
	}

	for _, id := range selectedIDs {
		if inv, ok := idMap[id]; ok {
			investorData = append(investorData, map[string]interface{}{
				"id":                inv.ID,
				"name":              inv.Name,
				"primaryLens":       inv.PrimaryLens,
				"signatureQuestion": inv.SignatureQuestion,
				"avatar":            inv.Avatar,
				"bio":               inv.Bio,
			})
		}
	}

	return map[string]interface{}{
		"pitchReceived": true,
		"investors":     investorData,
		"message":       "Your pitch has been analyzed. The investor panel is ready.",
		"analysis": map[string]interface{}{
			"transcription": analysis.Transcription,
			"feedback":      analysis.Feedback,
			"strengths":     analysis.Strengths,
			"weaknesses":    analysis.Weaknesses,
			"overallScore":  analysis.OverallScore,
			"clarity":       analysis.Clarity,
			"confidence":    analysis.Confidence,
			"persuasion":    analysis.Persuasion,
		},
	}, nil
}

// ============================================
// WAR ROOM - RESPOND TO INVESTOR AUDIO
// ============================================

func (s *AssessmentService) RespondToInvestorAudio(assessmentID string, investorID string, audioBase64 string, mimeType string) (*models.InvestorScorecard, map[string]interface{}, error) {
	var assessment models.Assessment
	if err := db.DB.Where("id = ?", assessmentID).First(&assessment).Error; err != nil {
		return nil, nil, errors.New("assessment not found")
	}

	investor, ok := s.DataManager.InvestorMap[investorID]
	if !ok {
		return nil, nil, errors.New("invalid investor ID")
	}

	// Analyze audio via Gemini
	analysis, err := s.AIService.AnalyzeInvestorResponseAudio(
		audioBase64,
		mimeType,
		investor.Name,
		investor.PrimaryLens,
		investor.BiasTraitName,
		investor.SignatureQuestion,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to analyze investor response audio: %w", err)
	}

	// Calculate deal decision using the audio analysis scores
	hasRedFlag := len(analysis.RedFlags) > 0
	redFlagJSON, _ := json.Marshal(analysis.RedFlags)

	capitalAsked := 100000.0
	equityOffered := 10.0

	deal := CalculateDealDecision(analysis.PrimaryScore, analysis.BiasTraitScore, hasRedFlag, capitalAsked, equityOffered)
	dealProposedJSON, _ := json.Marshal(deal)

	scorecard := &models.InvestorScorecard{
		ID:               uuid.New().String(),
		AssessmentID:     assessmentID,
		InvestorID:       investorID,
		InvestorName:     investor.Name,
		PrimaryScore:     &analysis.PrimaryScore,
		BiasTraitScore:   &analysis.BiasTraitScore,
		BiasTraitName:    investor.BiasTraitName,
		RedFlag:          hasRedFlag,
		RedFlagReasons:   redFlagJSON,
		DealDecision:     deal.Decision,
		DealProposed:     dealProposedJSON,
		Question:         investor.SignatureQuestion,
		ParticipantResp:  analysis.Transcription,
		InvestorReaction: analysis.Reaction,
	}

	db.DB.Create(scorecard)

	// Context for TTS
	ctx := context.Background()
	audioBase64Res, ttsErr := s.TTSService.GenerateVoice(ctx, analysis.Reaction, investor.Gender)
	if ttsErr != nil {
		fmt.Printf("Warning: failed to generate TTS: %v\n", ttsErr)
	}

	// Return extra analysis data alongside the scorecard
	analysisData := map[string]interface{}{
		"transcription": analysis.Transcription,
		"audioBase64":   audioBase64Res,
	}

	return scorecard, analysisData, nil
}

// ============================================
// GENERATE REPORT
// ============================================

func (s *AssessmentService) GenerateReport(assessmentID string) (*models.Report, error) {
	// First check if report already exists
	var existingReport models.Report
	err := db.DB.Where("assessmentId = ? AND reportType = ?", assessmentID, "FINAL").Order("generatedAt DESC").First(&existingReport).Error
	if err == nil {
		return &existingReport, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("failed to fetch existing report: %w", err)
	}

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
		var strengths, weaknesses []string
		json.Unmarshal(cs.Strengths, &strengths)
		json.Unmarshal(cs.Weaknesses, &weaknesses)

		compResults[cs.CompetencyCode] = &CompetencyResult{
			Code:            cs.CompetencyCode,
			Name:            cs.CompetencyName,
			WeightedAverage: cs.WeightedAverage,
			Category:        cs.Category,
			Strengths:       strengths,
			Weaknesses:      weaknesses,
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
			"strengths":       r.Strengths,
			"weaknesses":      r.Weaknesses,
			"keyInsight":      r.KeyInsight,
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
		StageName          string          `json:"stageName"`
		QuestionID         string          `json:"questionId"`
		QuestionText       string          `json:"questionText"`
		QuestionType       string          `json:"questionType"`
		Response           json.RawMessage `json:"response"`
		Proficiency        *int            `json:"proficiency"`
		AIFeedback         json.RawMessage `json:"aiFeedback"`
		SelectedOptionText string          `json:"selectedOptionText,omitempty"`
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

		// old entry removed

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
		// old entry removed
		entry := UserResponseEntry{
			StageName:          stageName,
			QuestionID:         r.QuestionID,
			QuestionText:       questionText,
			QuestionType:       r.QuestionType,
			Response:           r.ResponseData,
			Proficiency:        r.ProficiencyScore,
			AIFeedback:         r.AIEvaluation,
			SelectedOptionText: respText,
		}
		userResponseEntries = append(userResponseEntries, entry)
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
		map[string]interface{}{
			"revenue":   assessment.RevenueProjection,
			"expenses":  assessment.AccumulatedExpenses,
			"capital":   assessment.Capital,
			"source":    assessment.CapitalSource,
			"month":     assessment.SimulatedMonth,
			"team":      assessment.TeamState,
			"product":   assessment.ProductState,
			"customer":  assessment.CustomerState,
			"market":    assessment.MarketState,
			"financial": assessment.FinancialState,
			"budget":    assessment.BudgetAllocations,
		},
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

	if err := db.DB.Create(report).Error; err != nil {
		return nil, fmt.Errorf("failed to save generated report: %w", err)
	}

	log.Printf("[Report] Generated for assessment %s: type=%s, role=%s", assessmentID, entProfile.Type, roleFit.Role)
	return report, nil
}

// generateActionPlan creates specific action items based on weakest competencies
func (s *AssessmentService) generateActionPlan(ranked []*CompetencyResult) []map[string]interface{} {
	actions := make([]map[string]interface{}, 0)

	// Action guidance per competency
	guideline := map[string]string{
		"C1": "Enroll in a dynamic market research workshop or speak to 20 potential customers per week.",
		"C2": "Build a minimum viable prototype and test 'High Value' features first to avoid over-engineering.",
		"C3": "Practice 'Thinking in Scenarios'—before any decision, write down the 'Best Case', 'Worst Case', and 'Likely Case'.",
		"C4": "Review your Cash Flow daily. Deepen your understanding of EBITDA and burn rate management.",
		"C5": "Study 'Blue Ocean Strategy' and define a clear 2-year unfair advantage for your business.",
		"C6": "Engage in roleplay negotiation sessions. Learn to 'Anchor' and 'Walk Away' with confidence.",
		"C7": "Define your company's core values today. Hire only against these values and fire fast if they don't fit.",
		"C8": "Schedule 15 minutes of uninterrupted reflection at the end of every day to review your key decisions.",
		"C9": "Practice deliberate failure recovery — after every setback, write what you learned, what you'd do differently, and execute within 48 hours.",
	}

	// Focus on bottom 3 competencies
	for i := len(ranked) - 1; i >= 0 && len(actions) < 3; i-- {
		r := ranked[i]
		if r.WeightedAverage < CategoryAdvancedMin { // Anything below advanced benchmark needs work
			actionText := fmt.Sprintf("Focus on developing %s through deliberate practice.", r.Name)
			if g, ok := guideline[r.Code]; ok {
				actionText = g
			}
			actions = append(actions, map[string]interface{}{
				"competency": r.Code + " - " + r.Name,
				"category":   r.Category,
				"currentAvg": r.WeightedAverage,
				"action":     actionText,
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

func (s *AssessmentService) GetNegotiationOffers(assessmentID string) ([]map[string]interface{}, error) {
	// ALWAYS return the EXACT 3 predefined offers from the user's selected investors,
	// ignoring prior AI evaluations/scores, but filtering out rejected offers.
	var assessment models.Assessment
	if err := db.DB.Where("id = ?", assessmentID).First(&assessment).Error; err != nil {
		return nil, errors.New("assessment not found")
	}

	var state map[string]interface{}
	if len(assessment.DealResult) > 0 {
		json.Unmarshal(assessment.DealResult, &state)
	}

	var rejectedOffers []string
	if rej, ok := state["rejectedOffers"].([]interface{}); ok {
		for _, r := range rej {
			if str, ok := r.(string); ok {
				rejectedOffers = append(rejectedOffers, str)
			}
		}
	}

	isRejected := func(offerId string) bool {
		for _, r := range rejectedOffers {
			if r == offerId {
				return true
			}
		}
		return false
	}

	var investorList []string
	if len(assessment.SelectedInvestors) > 0 {
		json.Unmarshal(assessment.SelectedInvestors, &investorList)
	}

	// Ensure we have exactly 3 investor IDs
	for len(investorList) < 3 {
		investorList = append(investorList, "fallback_investor")
	}

	getInvName := func(id string) string {
		if inv, exists := s.DataManager.InvestorMap[id]; exists {
			return inv.Name
		}
		return "Lead Investor"
	}

	offers := make([]map[string]interface{}, 0, 3)

	if !isRejected("OFFER_1") {
		offers = append(offers, map[string]interface{}{
			"offerId":      "OFFER_1",
			"investorId":   investorList[0],
			"investorName": getInvName(investorList[0]),
			"capital":      1000000.0,
			"equity":       40.0,
			"message":      "I'm offering $1M for 40% of the company. I see potential, but the risk warrants a significant stake.",
		})
	}

	if !isRejected("OFFER_2") {
		offers = append(offers, map[string]interface{}{
			"offerId":      "OFFER_2",
			"investorId":   investorList[1],
			"investorName": getInvName(investorList[1]),
			"capital":      1000000.0,
			"equity":       55.0,
			"message":      "I'm willing to go in with $1M, but I want 55% of the company. I want to be the majority partner to guide this to success.",
		})
	}

	if !isRejected("OFFER_3") {
		offers = append(offers, map[string]interface{}{
			"offerId":      "OFFER_3",
			"investorId":   investorList[2],
			"investorName": getInvName(investorList[2]),
			"capital":      750000.0,
			"equity":       30.0,
			"message":      "I'll offer $750K for 30%. It's a fair valuation that keeps you motivated while giving me a solid chair at the table.",
		})
	}

	return offers, nil
}

// ============================================
// CounterNegotiate logic strictly follows the War Room SOP Flow
func (s *AssessmentService) CounterNegotiate(assessmentID string, investorID string, counterCapital float64, counterEquity float64) (map[string]interface{}, error) {
	var assessment models.Assessment
	if err := db.DB.Where("id = ?", assessmentID).First(&assessment).Error; err != nil {
		return nil, errors.New("assessment not found")
	}

	var investorList []string
	if len(assessment.SelectedInvestors) > 0 {
		json.Unmarshal(assessment.SelectedInvestors, &investorList)
	}
	for len(investorList) < 3 {
		investorList = append(investorList, "fallback_investor")
	}

	offerType := "OFFER_1"
	if investorID == investorList[1] {
		offerType = "OFFER_2"
	} else if investorID == investorList[2] {
		offerType = "OFFER_3"
	}

	var state map[string]interface{}
	if len(assessment.DealResult) > 0 {
		json.Unmarshal(assessment.DealResult, &state)
	} else {
		state = make(map[string]interface{})
	}

	roundKey := "round_" + offerType
	round := 1
	if r, ok := state[roundKey].(float64); ok {
		round = int(r) + 1
	}
	state[roundKey] = float64(round) // Using float64 because JSON stores numbers as float64

	var response map[string]interface{}

	switch offerType {
	case "OFFER_1":
		if round == 1 {
			response = map[string]interface{}{
				"accepted": false,
				"message":  "I can reduce to 35%, but I want milestone-based capital release.",
				"capital":  1000000.0,
				"equity":   35.0,
			}
		} else {
			response = map[string]interface{}{
				"accepted": false,
				"message":  "My final is 35% at milestone-based capital release. Risk is too high. Take it or leave it.",
				"capital":  1000000.0,
				"equity":   35.0,
				"isFinal":  true,
			}
		}

	case "OFFER_2":
		// Option 2 only has 1 round
		response = map[string]interface{}{
			"accepted": false,
			"message":  "Best I can do is 50%. Take it or leave it.",
			"capital":  1000000.0,
			"equity":   50.0,
			"isFinal":  true,
		}

	case "OFFER_3":
		if round == 1 {
			response = map[string]interface{}{
				"accepted": false,
				"message":  "I can increase to $800K for 30%.",
				"capital":  800000.0,
				"equity":   30.0,
			}
		} else {
			response = map[string]interface{}{
				"accepted": true,
				"message":  "Agrees",
				"capital":  850000.0,
				"equity":   30.0,
			}
		}
	}

	assessment.DealResult, _ = json.Marshal(state)
	db.DB.Save(&assessment)

	return response, nil
}

type NegotiationAudioResponse struct {
	Transcription string  `json:"transcription"`
	Message       string  `json:"message"`
	Accepted      bool    `json:"accepted"`
	IsFinal       bool    `json:"isFinal"`
	Capital       float64 `json:"capital"`
	Equity        float64 `json:"equity"`
	AudioBase64   string  `json:"audioBase64,omitempty"`
}

func (s *AssessmentService) CounterNegotiateAudio(assessmentID string, investorID string, audioBase64 string) (*NegotiationAudioResponse, error) {
	var assessment models.Assessment
	if err := db.DB.Where("id = ?", assessmentID).First(&assessment).Error; err != nil {
		return nil, errors.New("assessment not found")
	}

	investor, ok := s.DataManager.InvestorMap[investorID]
	if !ok {
		return nil, errors.New("investor not found")
	}

	var investorList []string
	if len(assessment.SelectedInvestors) > 0 {
		json.Unmarshal(assessment.SelectedInvestors, &investorList)
	}

	offerType := "OFFER_1"
	if investorID == investorList[1] {
		offerType = "OFFER_2"
	} else if investorID == investorList[2] {
		offerType = "OFFER_3"
	}

	var state map[string]interface{}
	if len(assessment.DealResult) > 0 {
		json.Unmarshal(assessment.DealResult, &state)
	} else {
		state = make(map[string]interface{})
	}

	roundKey := "round_" + offerType
	round := 0
	if r, ok := state[roundKey].(float64); ok {
		round = int(r)
	}
	round++
	state[roundKey] = float64(round)

	// AIService to transcribe and analyze the counter offer
	systemPrompt := fmt.Sprintf(`You are %s, an investor in KK's War Room 2.0.
Your investor persona/bio: %s
The founder is negotiating with you for an investment.
Current Round: %d

Your task:
1. Transcribe the founder's counter-offer accurately.
2. Decide if the offer is reasonable based on your investor persona and the current round.
3. If reasonable, you can ACCEPT (set accepted: true).
4. If not, you can COUNTER or provide a final "Take it or leave it" offer.
5. If it's the final round (Round 2 for most), you must either accept or give a final offer.

Respond in valid JSON:
{
  "transcription": "<exact transcription>",
  "message": "<your spoken response to the founder>",
  "accepted": <true/false>,
  "isFinal": <true/false>,
  "capital": <the capital amount in USD you are agreeing to or countering with>,
  "equity": <the equity percentage you are asking for>
}`, investor.Name, investor.Bio, round)

	userText := "Listen to the founder's counter offer and respond."
	aiResp, err := s.AIService.CallWithAudio(systemPrompt, userText, audioBase64, "audio/webm")
	if err != nil {
		return nil, fmt.Errorf("AI analysis failed: %w", err)
	}

	var result NegotiationAudioResponse
	content := aiResp.Content
	start := strings.Index(content, "{")
	end := strings.LastIndex(content, "}")
	if start >= 0 && end > start {
		if err := json.Unmarshal([]byte(content[start:end+1]), &result); err != nil {
			log.Printf("[AssessmentService] CounterNegotiateAudio parse error: %v", err)
			return nil, errors.New("failed to parse AI response")
		}
	} else {
		return nil, errors.New("invalid AI response format")
	}

	// Persist state
	assessment.DealResult, _ = json.Marshal(state)
	db.DB.Save(&assessment)

	// Sync capital/equity if accepted
	if result.Accepted {
		state["acceptedOffer"] = offerType
		state["finalCapital"] = result.Capital
		state["finalEquity"] = result.Equity
		assessment.DealResult, _ = json.Marshal(state)

		// Logic similar to AcceptDeal
		assessment.RevenueProjection += int64(result.Capital)
		assessment.Capital += result.Capital
		assessment.CapitalSource = fmt.Sprintf("War Room Deal: %s for %.1f%%", investor.Name, result.Equity)

		db.DB.Save(&assessment)

		// Broadcast updated leaderboard
		batchSvc := NewBatchService()
		entries, _ := batchSvc.GetLeaderboard(assessment.BatchCode)
		if len(entries) > 0 {
			iEntries := make([]interface{}, len(entries))
			for i, e := range entries {
				iEntries[i] = e
			}
			broadcast.Broadcast(assessment.BatchCode, iEntries)
		}
	}

	// Generate Voice for the investor message
	voiceBase64, err := s.TTSService.GenerateVoice(context.Background(), result.Message, investor.Gender)
	if err == nil {
		result.AudioBase64 = voiceBase64
	}

	return &result, nil
}

// ============================================
// RESTART ASSESSMENT
// ============================================

func (s *AssessmentService) RestartAssessment(assessmentID string) (*models.Assessment, error) {
	var assessment models.Assessment
	if err := db.DB.Where("id = ?", assessmentID).First(&assessment).Error; err != nil {
		return nil, errors.New("assessment not found")
	}

	now := time.Now()
	firstStage := "STAGE_NEG2_IDEATION"
	firstQ := s.DataManager.GetFirstQuestionInStage(firstStage)

	// Increment restart count
	assessment.RestartCount++
	assessment.CurrentStage = firstStage
	assessment.CurrentQuestionID = firstQ
	assessment.SimulatedMonth = 0
	assessment.Status = "IN_PROGRESS"
	assessment.LastActiveAt = &now
	assessment.RevenueProjection = 0 // Reset revenue

	// Reset states
	assessment.FinancialState = json.RawMessage(`{"capital": 0, "revenue": 0, "burnRate": 0, "runway": 0, "equity": 100, "debt": 0}`)
	assessment.TeamState = json.RawMessage(`{"size": 1, "morale": 100, "roles": ["founder"]}`)
	assessment.CustomerState = json.RawMessage(`{"count": 0, "retention": 0, "satisfaction": 0}`)
	assessment.ProductState = json.RawMessage(`{"quality": 0, "features": 0, "mvpLaunched": false}`)
	assessment.MarketState = json.RawMessage(`{"competition": "unknown", "positioning": "undefined"}`)
	assessment.Capital = 0
	assessment.CapitalSource = ""
	assessment.BudgetAllocations = json.RawMessage(`{}`)

	if err := db.DB.Save(&assessment).Error; err != nil {
		return nil, fmt.Errorf("failed to restart assessment: %w", err)
	}

	// Reset competency scores
	db.DB.Model(&models.CompetencyScore{}).Where("assessmentId = ?", assessmentID).Updates(map[string]interface{}{
		"stageScores":     json.RawMessage(`{}`),
		"weightedAverage": 0,
		"category":        "P1",
	})

	// Create a new stage record for the first stage
	stageData := s.DataManager.GetStage(firstStage)
	stageRecord := &models.Stage{
		ID:           uuid.New().String(),
		AssessmentID: assessment.ID,
		StageName:    firstStage,
		StageNumber:  stageData.StageNumber,
		StartedAt:    &now,
	}
	db.DB.Create(stageRecord)

	log.Printf("[Assessment] Restarted: id=%s, count=%d", assessment.ID, assessment.RestartCount)
	return &assessment, nil
}

// ============================================
// ACCEPT DEAL
// ============================================

func (s *AssessmentService) RejectOffer(assessmentID string, offerID string) error {
	var assessment models.Assessment
	if err := db.DB.Where("id = ?", assessmentID).First(&assessment).Error; err != nil {
		return errors.New("assessment not found")
	}

	var state map[string]interface{}
	if len(assessment.DealResult) > 0 {
		json.Unmarshal(assessment.DealResult, &state)
	} else {
		state = make(map[string]interface{})
	}

	var rejectedOffers []string
	if rej, ok := state["rejectedOffers"].([]interface{}); ok {
		for _, r := range rej {
			if str, ok := r.(string); ok {
				rejectedOffers = append(rejectedOffers, str)
			}
		}
	}

	// Add if not exists
	found := false
	for _, r := range rejectedOffers {
		if r == offerID {
			found = true
			break
		}
	}
	if !found {
		rejectedOffers = append(rejectedOffers, offerID)
	}

	state["rejectedOffers"] = rejectedOffers
	assessment.DealResult, _ = json.Marshal(state)
	return db.DB.Save(&assessment).Error
}

func (s *AssessmentService) AcceptDeal(assessmentID string, investorID string, capital float64, equity float64) (*models.Assessment, error) {
	var assessment models.Assessment
	if err := db.DB.Where("id = ?", assessmentID).First(&assessment).Error; err != nil {
		return nil, errors.New("assessment not found")
	}

	// Update revenue with accepted capital
	assessment.RevenueProjection += int64(capital)
	assessment.Capital += capital
	assessment.CapitalSource = fmt.Sprintf("War Room Deal: %s for %.1f%%", s.DataManager.InvestorMap[investorID].Name, equity)

	if err := db.DB.Save(&assessment).Error; err != nil {
		return nil, fmt.Errorf("failed to save accepted deal: %w", err)
	}

	// Broadcast updated leaderboard
	batchSvc := NewBatchService()
	entries, _ := batchSvc.GetLeaderboard(assessment.BatchCode)
	iEntries := make([]interface{}, len(entries))
	for i, e := range entries {
		iEntries[i] = e
	}
	broadcast.Broadcast(assessment.BatchCode, iEntries)

	return &assessment, nil
}

// ============================================
// HANDLE BUYOUT
// ============================================

func (s *AssessmentService) HandleBuyout(assessmentID string, company string, amount float64) (*models.Assessment, error) {
	var assessment models.Assessment
	if err := db.DB.Where("id = ?", assessmentID).First(&assessment).Error; err != nil {
		return nil, errors.New("assessment not found")
	}

	now := time.Now()
	assessment.BuyoutChosen = true
	assessment.Status = "COMPLETED"
	assessment.CompletedAt = &now
	assessment.LastActiveAt = &now
	assessment.Capital = amount
	if company != "" {
		assessment.CapitalSource = fmt.Sprintf("Buyout by %s", company)
	}

	if err := db.DB.Save(&assessment).Error; err != nil {
		return nil, fmt.Errorf("failed to process buyout: %w", err)
	}

	// Trigger report generation (async)
	go s.GenerateReport(assessmentID)

	log.Printf("[Assessment] Buyout Chosen: id=%s by=%s amount=%v", assessment.ID, company, amount)
	return &assessment, nil
}

// ============================================
// HANDLE WALKOUT
// ============================================

func (s *AssessmentService) HandleWalkout(assessmentID string) (*models.Assessment, error) {
	var assessment models.Assessment
	if err := db.DB.Where("id = ?", assessmentID).First(&assessment).Error; err != nil {
		return nil, errors.New("assessment not found")
	}

	now := time.Now()
	assessment.Status = "COMPLETED"
	assessment.CompletedAt = &now
	assessment.LastActiveAt = &now

	if err := db.DB.Save(&assessment).Error; err != nil {
		return nil, fmt.Errorf("failed to process walkout: %w", err)
	}

	// Trigger report generation (async)
	go s.GenerateReport(assessmentID)

	log.Printf("[Assessment] Walkout from all offers: id=%s", assessment.ID)
	return &assessment, nil
}

func (s *AssessmentService) GetScorecards(assessmentID string) ([]models.InvestorScorecard, error) {
	var scorecards []models.InvestorScorecard
	if err := db.DB.Where("assessment_id = ?", assessmentID).Find(&scorecards).Error; err != nil {
		return nil, err
	}
	return scorecards, nil
}
