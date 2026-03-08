package services

// phase_service.go — New v2 phase-level submission, character selection, and scenario handling.
// All methods are on *AssessmentService so they share data manager, AI service, etc.

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"war-room-backend/internal/broadcast"
	"war-room-backend/internal/db"
	"war-room-backend/internal/models"

	"github.com/google/uuid"
)

// ============================================
// PHASE SUBMIT RESULT
// ============================================

type PhaseSubmitResult struct {
	StageID           string             `json:"stageId"`
	Responses         []PhaseResponseOut `json:"responses"`
	StageScores       map[string]float64 `json:"stageScores"`       // competency code → avg score for this stage
	RevenueProjection int64              `json:"revenueProjection"` // updated after this phase
	PhaseScenario     *PhaseScenarioOut  `json:"phaseScenario,omitempty"`
	NextStage         *NextStageInfo     `json:"nextStage,omitempty"`
	SimCompleted      bool               `json:"simCompleted"`
}

type PhaseResponseOut struct {
	QuestionID       string          `json:"questionId"`
	QuestionType     string          `json:"questionType"`
	ProficiencyScore int             `json:"proficiencyScore"`
	AIEvaluation     json.RawMessage `json:"aiEvaluation"`
}

type PhaseScenarioOut struct {
	ID            string `json:"id"`
	ScenarioTitle string `json:"scenarioTitle"`
	ScenarioSetup string `json:"scenarioSetup"`
	LeaderPrompt  string `json:"leaderPrompt"` // AI-generated challenge from leader
	LeaderID      string `json:"leaderId"`
	LeaderName    string `json:"leaderName"`
	FromStage     string `json:"fromStage"`
	ToStage       string `json:"toStage"`
}

// SubmitPhase collects all answers for a phase at once.
// MCQ answers are scored from pre-defined option proficiency (no AI).
// Open-text answers are sent to AI in a single batched call.
// Returns updated revenue projection and optional phase scenario question.
func (s *AssessmentService) SubmitPhase(assessmentID string, stageID string, responses map[string]json.RawMessage) (*PhaseSubmitResult, error) {
	// 1. Load assessment
	var assessment models.Assessment
	if err := db.DB.Where("id = ?", assessmentID).First(&assessment).Error; err != nil {
		return nil, errors.New("assessment not found")
	}
	if assessment.Status != "IN_PROGRESS" {
		return nil, errors.New("assessment is not in progress")
	}
	if assessment.CurrentStage != stageID {
		return nil, errors.New("stage mismatch")
	}

	// 2. Get stage data and questions
	stage := s.DataManager.GetStage(stageID)
	if stage == nil {
		return nil, fmt.Errorf("unknown stage: %s", stageID)
	}

	// 3. Get or create Stage record
	var stageRecord models.Stage
	if err := db.DB.Where("assessmentId = ? AND stageName = ?", assessmentID, stageID).First(&stageRecord).Error; err != nil {
		now := time.Now()
		stageRecord = models.Stage{
			ID:           uuid.New().String(),
			AssessmentID: assessmentID,
			StageName:    stageID,
			StageNumber:  stage.StageNumber,
			StartedAt:    &now,
		}
		db.DB.Create(&stageRecord)
	}

	// 4. Evaluate each response
	compDefs := s.DataManager.GetCompetencyDefs()

	// Collect open-text questions for batched AI eval
	type openTextTask struct {
		qID          string
		questionText string
		responseText string
		competencies []string
	}
	var openTextTasks []openTextTask

	// Map q_id → result
	responseResults := map[string]*PhaseResponseOut{}
	now := time.Now()

	for qID, rawResp := range responses {
		question := s.DataManager.GetQuestion(qID)
		if question == nil {
			log.Printf("[PhaseSubmit] Unknown question %s — skipping", qID)
			continue
		}

		var respMap map[string]interface{}
		json.Unmarshal(rawResp, &respMap)

		out := &PhaseResponseOut{
			QuestionID:   qID,
			QuestionType: question.Type,
		}

		switch question.Type {
		case "multiple_choice", "scenario":
			// Pre-defined proficiency — zero AI cost
			selectedOptID, _ := respMap["selectedOptionId"].(string)
			proficiency := 1
			signal := ""
			warning := ""
			feedback := "Response recorded."
			for _, opt := range question.Options {
				if opt.ID == selectedOptID {
					proficiency = opt.Proficiency
					signal = opt.Signal
					warning = opt.Warning
					feedback = opt.Feedback
					if feedback == "" {
						feedback = fmt.Sprintf("You chose: %s", opt.Text)
					}
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

		case "open_text":
			responseText, _ := respMap["text"].(string)
			openTextTasks = append(openTextTasks, openTextTask{
				qID:          qID,
				questionText: question.Text,
				responseText: responseText,
				competencies: question.Assess,
			})
			// Placeholder — will be filled after AI batch
			out.ProficiencyScore = 0

		case "budget_allocation":
			proficiency := s.evaluateBudgetAllocation(respMap)
			out.ProficiencyScore = proficiency
			out.AIEvaluation, _ = json.Marshal(map[string]interface{}{
				"proficiency": proficiency,
				"feedback":    "Budget allocation evaluated.",
				"source":      "predefined",
			})
		}

		responseResults[qID] = out
	}

	// 5. Batch AI evaluation for open-text questions
	for _, task := range openTextTasks {
		eval, err := s.AIService.EvaluateOpenText(task.questionText, task.responseText, task.competencies, compDefs)
		if err != nil {
			log.Printf("[PhaseSubmit] AI eval error for q=%s: %v", task.qID, err)
			eval = &TextEvaluation{Proficiency: 2, Feedback: "Response recorded."}
		}
		if out, ok := responseResults[task.qID]; ok {
			out.ProficiencyScore = eval.Proficiency
			out.AIEvaluation, _ = json.Marshal(eval)
		}
	}

	// 6. Persist all responses (marked as committed)
	stageWeights := s.DataManager.GetStageWeights(stageID)
	for qID, rawResp := range responses {
		out, ok := responseResults[qID]
		if !ok {
			continue
		}
		question := s.DataManager.GetQuestion(qID)
		if question == nil {
			continue
		}

		competenciesJSON, _ := json.Marshal(question.Assess)
		relevantWeights := map[string]int{}
		for _, code := range question.Assess {
			if w, ok2 := stageWeights[code]; ok2 {
				relevantWeights[code] = w
			}
		}
		weightsJSON, _ := json.Marshal(relevantWeights)

		proficiency := out.ProficiencyScore
		response := &models.Response{
			ID:                   uuid.New().String(),
			AssessmentID:         assessmentID,
			StageID:              stageRecord.ID,
			QuestionID:           qID,
			QuestionType:         question.Type,
			ResponseData:         rawResp,
			IsPending:            false,
			ProficiencyScore:     &proficiency,
			CompetenciesAssessed: competenciesJSON,
			StageWeights:         weightsJSON,
			AIEvaluation:         out.AIEvaluation,
			StartedAt:            now,
			AnsweredAt:           &now,
		}
		db.DB.Create(response)

		// Update competency scores
		s.updateCompetencyScores(assessmentID, stageID, question.Assess, proficiency)
	}

	// 7. Mark stage as complete
	completedAt := time.Now()
	stageRecord.CompletedAt = &completedAt
	stageRecord.QuestionsAns = len(responseResults)
	db.DB.Save(&stageRecord)

	// 8. Compute stage-level competency scores for this stage
	stageScores := s.computeStageScores(assessmentID, stageID)

	// 9. Update revenue projection
	revSvc := NewRevenueProjectionService()
	c4Score := stageScores["C4"]
	c5Score := stageScores["C5"]
	if c4Score == 0 {
		c4Score = 2.0
	}
	if c5Score == 0 {
		c5Score = 2.0
	}
	stageIndex := stage.StageNumber - 1
	revenueProjection := revSvc.ComputeRevenueProjection(stageIndex, c4Score, c5Score, assessment.FinancialState, assessment.CustomerState)

	// 10. Determine next stage
	nextStageID := s.DataManager.GetNextStageID(stageID)
	var nextStageInfo *NextStageInfo
	var simCompleted bool

	if nextStageID == "" {
		simCompleted = true
		assessment.Status = "COMPLETED"
		assessment.CompletedAt = &completedAt
	} else {
		nextStage := s.DataManager.GetStage(nextStageID)
		firstQ := s.DataManager.GetFirstQuestionInStage(nextStageID)
		nextStageInfo = &NextStageInfo{
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

	assessment.RevenueProjection = revenueProjection
	assessment.LastActiveAt = &now
	db.DB.Save(&assessment)

	// 11. Broadcast updated leaderboard to batch
	if assessment.BatchCode != "" {
		batchSvc := NewBatchService()
		go func(code string) {
			entries, err := batchSvc.GetLeaderboard(code)
			if err != nil {
				log.Printf("[PhaseSubmit] leaderboard fetch error: %v", err)
				return
			}
			// Convert to []interface{} for broadcast
			iEntries := make([]interface{}, len(entries))
			for i, e := range entries {
				iEntries[i] = e
			}
			broadcast.Broadcast(code, iEntries)
		}(assessment.BatchCode)
	}

	// 12. Build phase scenario if transitioning (not for war room or completion)
	// Now uses AI to generate a unified leader challenge based on user's responses
	var phaseScenarioOut *PhaseScenarioOut
	if nextStageInfo != nil && !simCompleted && nextStageID != "STAGE_4_WARROOM" {
		scenario, err := s.buildPhaseScenario(assessmentID, stageID, nextStageID, assessment.SelectedLeaders, responses, assessment.UserIdea)
		if err == nil && scenario != nil {
			phaseScenarioOut = scenario
		}
	}

	// Build response list
	responseList := make([]PhaseResponseOut, 0, len(responseResults))
	for _, v := range responseResults {
		responseList = append(responseList, *v)
	}

	return &PhaseSubmitResult{
		StageID:           stageID,
		Responses:         responseList,
		StageScores:       stageScores,
		RevenueProjection: revenueProjection,
		PhaseScenario:     phaseScenarioOut,
		NextStage:         nextStageInfo,
		SimCompleted:      simCompleted,
	}, nil
}

// computeStageScores returns avg proficiency per competency for this stage.
func (s *AssessmentService) computeStageScores(assessmentID, stageID string) map[string]float64 {
	var responses []models.Response
	db.DB.Where("assessmentId = ? AND stageId IN (SELECT id FROM stages WHERE assessmentId = ? AND stageName = ?)",
		assessmentID, assessmentID, stageID).Find(&responses)

	compTotals := map[string][]int{}
	for _, r := range responses {
		if r.ProficiencyScore == nil {
			continue
		}
		var comps []string
		json.Unmarshal(r.CompetenciesAssessed, &comps)
		for _, c := range comps {
			compTotals[c] = append(compTotals[c], *r.ProficiencyScore)
		}
	}

	result := map[string]float64{}
	for code, scores := range compTotals {
		sum := 0
		for _, v := range scores {
			sum += v
		}
		result[code] = float64(sum) / float64(len(scores))
	}
	return result
}

// buildPhaseScenario creates a unified leader challenge for the phase transition.
// It uses AI to generate a personalized challenge question based on the user's actual responses.
func (s *AssessmentService) buildPhaseScenario(assessmentID, fromStage, toStage string, selectedLeadersJSON json.RawMessage, responses map[string]json.RawMessage, userIdea string) (*PhaseScenarioOut, error) {
	// Get scenario template from simulation data (for context/setup)
	scenario := s.DataManager.GetPhaseTransitionScenario(fromStage, toStage)
	if scenario == nil {
		return nil, nil // No scenario defined for this transition
	}

	// Pick a leader (rotate based on stage number)
	var selectedLeaders []string
	json.Unmarshal(selectedLeadersJSON, &selectedLeaders)
	if len(selectedLeaders) == 0 {
		selectedLeaders = []string{"indira_nooyi", "jack_ma", "simon_sinek"}
	}

	// Pick leader by cycling through transitions
	var existingScenarios int64
	db.DB.Model(&models.PhaseScenario{}).Where("assessment_id = ?", assessmentID).Count(&existingScenarios)
	leaderIdx := int(existingScenarios) % len(selectedLeaders)
	leaderID := selectedLeaders[leaderIdx]

	// Get leader details
	leader := s.DataManager.GetLeader(leaderID)
	leaderName := leaderID
	leaderSpec := "Business Strategy"
	if leader != nil {
		leaderName = leader.Name
		if leader.Specialization != "" {
			leaderSpec = leader.Specialization
		}
	}

	// Build a summary of the user's responses for AI context
	var summaryLines []string
	for qID, rawResp := range responses {
		q := s.DataManager.GetQuestion(qID)
		if q == nil {
			continue
		}
		var respMap map[string]interface{}
		json.Unmarshal(rawResp, &respMap)
		switch q.Type {
		case "multiple_choice", "scenario":
			if optID, ok := respMap["selectedOptionId"].(string); ok {
				for _, opt := range q.Options {
					if opt.ID == optID {
						summaryLines = append(summaryLines, fmt.Sprintf("- Q: %s → Chose: %s", q.Text, opt.Text))
						break
					}
				}
			}
		case "open_text":
			if text, ok := respMap["text"].(string); ok && text != "" {
				truncated := text
				if len(truncated) > 200 {
					truncated = truncated[:200] + "..."
				}
				summaryLines = append(summaryLines, fmt.Sprintf("- Q: %s → %s", q.Text, truncated))
			}
		}
	}
	responsesSummary := strings.Join(summaryLines, "\n")
	if responsesSummary == "" {
		responsesSummary = "No detailed responses provided."
	}

	// Generate AI-powered leader challenge question
	aiQuestion, err := s.AIService.GenerateLeaderChallenge(
		fromStage,
		responsesSummary,
		userIdea,
		leaderName,
		leaderSpec,
	)
	if err != nil {
		log.Printf("[PhaseScenario] AI leader challenge generation error: %v", err)
		aiQuestion = scenario.LeaderPromptTemplate
	}

	prompt := fmt.Sprintf("%s asks: \"%s\"", leaderName, aiQuestion)

	// Save scenario record
	record := &models.PhaseScenario{
		ID:            uuid.New().String(),
		AssessmentID:  assessmentID,
		FromStage:     fromStage,
		ToStage:       toStage,
		LeaderID:      leaderID,
		LeaderName:    leaderName,
		ScenarioTitle: scenario.CaseTitle,
		ScenarioSetup: scenario.Setup,
		LeaderPrompt:  prompt,
	}
	db.DB.Create(record)

	return &PhaseScenarioOut{
		ID:            record.ID,
		ScenarioTitle: scenario.CaseTitle,
		ScenarioSetup: scenario.Setup,
		LeaderPrompt:  prompt,
		LeaderID:      leaderID,
		LeaderName:    leaderName,
		FromStage:     fromStage,
		ToStage:       toStage,
	}, nil
}

// ============================================
// ANSWER PHASE SCENARIO
// ============================================

type ScenarioAnswerResult struct {
	ProficiencyScore int    `json:"proficiencyScore"`
	AIFeedback       string `json:"aiFeedback"`
	NextStage        string `json:"nextStage"`
}

func (s *AssessmentService) AnswerPhaseScenario(assessmentID, fromStage, toStage, userResponse string) (*ScenarioAnswerResult, error) {
	// Find the phase scenario record
	var scenario models.PhaseScenario
	if err := db.DB.Where("assessment_id = ? AND from_stage = ? AND to_stage = ?", assessmentID, fromStage, toStage).
		Order("created_at DESC").First(&scenario).Error; err != nil {
		return nil, errors.New("phase scenario not found")
	}

	// Get scenario template for rubric
	tmpl := s.DataManager.GetPhaseTransitionScenario(fromStage, toStage)

	// Evaluate with AI
	compDefs := s.DataManager.GetCompetencyDefs()
	var competencies []string
	if tmpl != nil {
		competencies = tmpl.CompetenciesAssessed
	}

	eval, err := s.AIService.EvaluateOpenText(scenario.LeaderPrompt, userResponse, competencies, compDefs)
	if err != nil {
		log.Printf("[PhaseScenario] AI eval error: %v", err)
		eval = &TextEvaluation{Proficiency: 2, Feedback: "Response recorded."}
	}

	// Save response
	now := time.Now()
	scenario.UserResponse = userResponse
	scenario.ProficiencyScore = &eval.Proficiency
	scenario.AIFeedback = eval.Feedback
	scenario.AnsweredAt = &now
	db.DB.Save(&scenario)

	// Update competency scores if competencies are defined
	if len(competencies) > 0 {
		s.updateCompetencyScores(assessmentID, fromStage, competencies, eval.Proficiency)
	}

	return &ScenarioAnswerResult{
		ProficiencyScore: eval.Proficiency,
		AIFeedback:       eval.Feedback,
		NextStage:        toStage,
	}, nil
}

// ============================================
// CHARACTER SELECTION
// ============================================

type CharactersState struct {
	SelectedMentors   []string `json:"selectedMentors"`
	SelectedLeaders   []string `json:"selectedLeaders"`
	SelectedInvestors []string `json:"selectedInvestors"`
}

func (s *AssessmentService) GetCharacters(assessmentID string) (*CharactersState, error) {
	var assessment models.Assessment
	if err := db.DB.Where("id = ?", assessmentID).First(&assessment).Error; err != nil {
		return nil, errors.New("assessment not found")
	}

	state := &CharactersState{}
	json.Unmarshal(assessment.SelectedMentors, &state.SelectedMentors)
	json.Unmarshal(assessment.SelectedLeaders, &state.SelectedLeaders)
	json.Unmarshal(assessment.SelectedInvestors, &state.SelectedInvestors)
	return state, nil
}

func (s *AssessmentService) SetCharacters(assessmentID string, mentors, leaders, investors []string) error {
	var assessment models.Assessment
	if err := db.DB.Where("id = ?", assessmentID).First(&assessment).Error; err != nil {
		return errors.New("assessment not found")
	}

	assessment.SelectedMentors, _ = json.Marshal(mentors)
	assessment.SelectedLeaders, _ = json.Marshal(leaders)
	assessment.SelectedInvestors, _ = json.Marshal(investors)
	return db.DB.Save(&assessment).Error
}

// ============================================
// GENERATE AI END-OF-PHASE QUESTION
// ============================================

type GenerateAiQuestionRequest struct {
	StageID   string `json:"stageId"`
	Responses []struct {
		QuestionID string `json:"questionId"`
		Summary    string `json:"summary"`
	} `json:"responses"`
	UserIdea string `json:"userIdea"`
}

type GenerateAiQuestionResult struct {
	Question   string `json:"question"`
	LeaderName string `json:"leaderName"`
}

func (s *AssessmentService) GenerateAiQuestion(assessmentID string, req *GenerateAiQuestionRequest) (*GenerateAiQuestionResult, error) {
	var assessment models.Assessment
	if err := db.DB.Where("id = ?", assessmentID).First(&assessment).Error; err != nil {
		return nil, errors.New("assessment not found")
	}

	// Pick a leader from the participant's selected leaders
	var selectedLeaders []string
	json.Unmarshal(assessment.SelectedLeaders, &selectedLeaders)
	if len(selectedLeaders) == 0 {
		selectedLeaders = []string{"indira_nooyi", "jack_ma", "simon_sinek"}
	}

	// Rotate leader per stage
	stageData := s.DataManager.GetStage(req.StageID)
	leaderIdx := 0
	if stageData != nil {
		leaderIdx = (stageData.StageNumber - 1) % len(selectedLeaders)
	}
	leaderID := selectedLeaders[leaderIdx]

	leader := s.DataManager.GetLeader(leaderID)
	leaderName := leaderID
	leaderSpec := "Business Strategy"
	if leader != nil {
		leaderName = leader.Name
		if leader.Specialization != "" {
			leaderSpec = leader.Specialization
		}
	}

	// Build responses summary
	var summaryLines []string
	for _, r := range req.Responses {
		if r.Summary != "" && r.Summary != "(not answered)" {
			summaryLines = append(summaryLines, fmt.Sprintf("- %s", r.Summary))
		}
	}
	responsesSummary := strings.Join(summaryLines, "\n")
	if responsesSummary == "" {
		responsesSummary = "No responses provided."
	}

	userIdea := req.UserIdea
	if userIdea == "" {
		userIdea = assessment.UserIdea
	}

	question, err := s.AIService.GenerateLeaderChallenge(
		req.StageID,
		responsesSummary,
		userIdea,
		leaderName,
		leaderSpec,
	)
	if err != nil {
		question = fmt.Sprintf("Given your decisions this phase, how will you ensure your approach remains sustainable as %s scales?", userIdea)
	}

	return &GenerateAiQuestionResult{
		Question:   question,
		LeaderName: leaderName,
	}, nil
}
