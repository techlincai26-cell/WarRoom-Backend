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
	IsCheckpoint  bool   `json:"isCheckpoint,omitempty"`
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

	// Capture user idea if this is the ideation phase
	if stageID == "STAGE_NEG2_IDEATION" {
		if val, ok := responses["Q_NEG2_1"]; ok {
			var respMap map[string]interface{}
			json.Unmarshal(val, &respMap)
			if idea, exists := respMap["text"].(string); exists && idea != "" {
				assessment.UserIdea = idea
			}
		}
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

	// 4. Prepare tasks for evaluation
	compDefs := s.DataManager.GetCompetencyDefs()
	_ = compDefs // compDefs is needed for AI batch later
	var openTextTasks []BatchEvaluationItem
	responseResults := map[string]*PhaseResponseOut{}
	var stageExpenses int64

	// Initialize missing variables for loop later
	stageWeights := s.DataManager.GetStageWeights(stageID)
	allAssessedCompetencies := make(map[string]bool)
	var dbResponses []*models.Response
	now := time.Now()
	stageProficiencies := make(map[string][]int)

	for qID, rawResp := range responses {
		question := s.DataManager.GetQuestion(qID)
		if question == nil {
			continue
		}

		var respMap map[string]interface{}
		json.Unmarshal(rawResp, &respMap)

		out := &PhaseResponseOut{
			QuestionID:   qID,
			QuestionType: question.Type,
		}

		switch question.Type {
		case "multiple_choice", "scenario", "dynamic_scenario":
			selectedOptID, _ := respMap["selectedOptionId"].(string)
			proficiency := 1
			feedback := "Response recorded."
			var signal, warning string

			if question.Type == "dynamic_scenario" {
				var ds models.DynamicScenario
				if err := db.DB.Where("assessment_id = ? AND question_id = ?", assessmentID, qID).First(&ds).Error; err == nil {
					var options []models.SimOption
					json.Unmarshal(ds.Options, &options)
					for _, opt := range options {
						if opt.ID == selectedOptID {
							proficiency = opt.Proficiency
							feedback = opt.Feedback
							if val, ok := opt.Impact["expense"]; ok {
								if exp, ok2 := val.(float64); ok2 {
									stageExpenses += int64(exp)
								}
							}
							break
						}
					}
				}
			} else {
				for _, opt := range question.Options {
					if opt.ID == selectedOptID {
						proficiency = opt.Proficiency
						signal = opt.Signal
						warning = opt.Warning
						feedback = opt.Feedback
						if val, ok := opt.Impact["expense"]; ok {
							if exp, ok2 := val.(float64); ok2 {
								stageExpenses += int64(exp)
							}
						}
						break
					}
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
			openTextTasks = append(openTextTasks, BatchEvaluationItem{
				QuestionID:   qID,
				QuestionText: question.Text,
				ResponseText: responseText,
				Competencies: question.Assess,
			})
			out.ProficiencyScore = 0 // Placeholder

		case "budget_allocation":
			proficiency := s.evaluateBudgetAllocation(respMap)
			out.ProficiencyScore = proficiency

			if allocs, ok := respMap["allocations"]; ok {
				bBytes, _ := json.Marshal(allocs)
				assessment.BudgetAllocations = bBytes
				if assessment.Capital == 0 {
					assessment.Capital = 50000
				}
			}

			out.AIEvaluation, _ = json.Marshal(map[string]interface{}{
				"proficiency": proficiency,
				"feedback":    "Budget allocation evaluated.",
				"source":      "predefined",
			})
		}
		responseResults[qID] = out
	}

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
		// For metrics, we collect current scores. Open-text starts at 0.
		stageProficiencies[stageID] = append(stageProficiencies[stageID], out.ProficiencyScore)
	}

	if len(dbResponses) > 0 {
		db.DB.Create(&dbResponses)
	}

	// Collect competency list for background processing
	var compList []string
	for c := range allAssessedCompetencies {
		compList = append(compList, c)
	}

	// 8. Update Stage & Assessment state immediately for the fast path
	stageRecord.QuestionsAns = len(responseResults)
	stageRecord.CompletedAt = &now
	db.DB.Save(&stageRecord)

	// Determine next stage
	nextStageID := s.DataManager.GetNextStageID(stageID)
	var nextStageInfo *NextStageInfo
	var simCompleted bool
	var phaseScenario *PhaseScenarioOut

	if assessment.BuyoutChosen || nextStageID == "" {
		simCompleted = true
		assessment.Status = "COMPLETED"
		assessment.CompletedAt = &now
	} else {
		nextStage := s.DataManager.GetStage(nextStageID)
		firstQ := s.DataManager.GetFirstQuestionInStage(nextStageID)
		nextStageInfo = &NextStageInfo{
			ID: nextStageID, Name: nextStage.Name, Title: nextStage.Title, StageNumber: nextStage.StageNumber,
			Competencies: nextStage.Competencies, FirstQuestion: firstQ,
		}

		// Check for phase transition scenario (AI challenge)
		scenario, _ := s.buildPhaseScenario(assessmentID, stageID, nextStageID, assessment.SelectedLeaders, responses, assessment.UserIdea)
		if scenario != nil {
			phaseScenario = scenario
		} else {
			// Only advance state if there's no scenario (scenarios manage their own transition)
			assessment.CurrentStage = nextStageID
			assessment.CurrentQuestionID = firstQ
			if len(nextStage.SimulatedMonths) > 0 {
				assessment.SimulatedMonth = nextStage.SimulatedMonths[0]
			}
			go s.PreGenerateDynamicScenarios(assessmentID, nextStageID)
		}
	}

	assessment.AccumulatedExpenses += stageExpenses

	// Compute revenue projection synchronously so the frontend gets an accurate value.
	// We update competency scores first, then compute revenue based on the user's
	// actual proficiency — this ensures differentiated leaderboard values per user.
	s.BatchUpdateCompetencyScores(assessmentID, stageID, stageProficiencies, compList)

	stageData := s.DataManager.GetStage(stageID)
	allCompScores := s.computeAllCompetencyScores(assessmentID)
	avgProficiency, totalResponses := s.getAssessmentProficiencyStats(assessmentID)

	revSvc := NewRevenueProjectionService()
	revenueProjection := revSvc.ComputeRevenueProjection(stageData.StageNumber, allCompScores, avgProficiency, totalResponses)

	revenueProjection -= assessment.AccumulatedExpenses
	if revenueProjection < 0 {
		revenueProjection = 0
	}

	assessment.RevenueProjection = revenueProjection
	assessment.LastActiveAt = &now
	db.DB.Save(&assessment)

	// 9. BACKGROUND HEAVY PROCESSING: AI Eval, Metrics Recalc, and Leaderboard Broadcast
	go s.processPhaseResultsAsync(assessmentID, stageID, openTextTasks, allAssessedCompetencies, stageProficiencies, compList)

	responseList := make([]PhaseResponseOut, 0, len(responseResults))
	for _, v := range responseResults {
		responseList = append(responseList, *v)
	}

	// For the fast path, we might return partially empty scores (will be updated via WebSocket)
	return &PhaseSubmitResult{
		StageID: stageID, Responses: responseList, StageScores: map[string]float64{},
		RevenueProjection: revenueProjection, NextStage: nextStageInfo, SimCompleted: simCompleted,
		PhaseScenario: phaseScenario,
	}, nil
}

// processPhaseResultsAsync handles AI evaluation of open-text responses and, if any
// existed, recomputes competency scores and revenue with the refined AI-scored proficiency.
// Revenue and competency scores are already computed synchronously in SubmitPhase,
// so this is a refinement pass that also broadcasts leaderboard updates.
func (s *AssessmentService) processPhaseResultsAsync(
	assessmentID, stageID string,
	openTextTasks []BatchEvaluationItem,
	allAssessedCompetencies map[string]bool,
	stageProficiencies map[string][]int,
	compList []string,
) {
	// 1. Batch AI evaluation for open-text if any
	hasOpenText := len(openTextTasks) > 0
	if hasOpenText {
		compDefs := s.DataManager.GetCompetencyDefs()
		batchEvals, err := s.AIService.BatchEvaluateOpenText(openTextTasks, compDefs)
		if err == nil {
			for qID, eval := range batchEvals {
				aiEvalJSON, _ := json.Marshal(eval)
				db.DB.Model(&models.Response{}).
					Where("assessmentId = ? AND questionId = ?", assessmentID, qID).
					Updates(map[string]interface{}{
						"proficiencyScore": eval.Proficiency,
						"aiEvaluation":     aiEvalJSON,
					})
			}
		}
	}

	// 2. Recompute competency scores and revenue if AI refined open-text scores
	if hasOpenText {
		s.BatchUpdateCompetencyScores(assessmentID, stageID, stageProficiencies, compList)

		// 3. Update Revenue Projection with refined AI scores
		var assessment models.Assessment
		db.DB.First(&assessment, "id = ?", assessmentID)

		stageData := s.DataManager.GetStage(stageID)
		allCompScores := s.computeAllCompetencyScores(assessmentID)
		avgProficiency, totalResponses := s.getAssessmentProficiencyStats(assessmentID)

		revSvc := NewRevenueProjectionService()
		revenueProjection := revSvc.ComputeRevenueProjection(stageData.StageNumber, allCompScores, avgProficiency, totalResponses)

		revenueProjection -= assessment.AccumulatedExpenses
		if revenueProjection < 0 {
			revenueProjection = 0
		}

		assessment.RevenueProjection = revenueProjection
		db.DB.Save(&assessment)
	}

	// 4. Broadcast updated leaderboard (always)
	var assessmentForBroadcast models.Assessment
	db.DB.First(&assessmentForBroadcast, "id = ?", assessmentID)
	batchSvc := NewBatchService()
	entries, _ := batchSvc.GetLeaderboard(assessmentForBroadcast.BatchCode)
	iEntries := make([]interface{}, len(entries))
	for i, e := range entries {
		iEntries[i] = e
	}
	broadcast.Broadcast(assessmentForBroadcast.BatchCode, iEntries)
}

// Optimized helper methods using SQL aggregations where possible

func (s *AssessmentService) computeStageScores(assessmentID string, stageID string) map[string]float64 {
	var stage models.Stage
	if err := db.DB.Where("assessmentId = ? AND stageName = ?", assessmentID, stageID).First(&stage).Error; err != nil {
		return map[string]float64{}
	}

	var results []struct {
		CompetenciesAssessed json.RawMessage
		ProficiencyScore     int
	}
	db.DB.Model(&models.Response{}).
		Where("assessmentId = ? AND stageId = ? AND proficiencyScore IS NOT NULL", assessmentID, stage.ID).
		Select("competencies_assessed, proficiency_score").
		Scan(&results)

	compTotals := map[string][]int{}
	for _, r := range results {
		var competencies []string
		json.Unmarshal(r.CompetenciesAssessed, &competencies)
		for _, competency := range competencies {
			compTotals[competency] = append(compTotals[competency], r.ProficiencyScore)
		}
	}

	stageScores := map[string]float64{}
	for competency, scores := range compTotals {
		sum := 0
		for _, score := range scores {
			sum += score
		}
		stageScores[competency] = float64(sum) / float64(len(scores))
	}

	return stageScores
}

func (s *AssessmentService) computeAllCompetencyScores(assessmentID string) map[string]float64 {
	// We still need to parse JSON CompetenciesAssessed, which is hard in pure SQL without specialized functions.
	// But we can at least fetch only what we need.
	var results []struct {
		CompetenciesAssessed json.RawMessage
		ProficiencyScore     int
	}
	db.DB.Model(&models.Response{}).Where("assessmentId = ? AND proficiencyScore IS NOT NULL", assessmentID).
		Select("competencies_assessed, proficiency_score").Scan(&results)

	compTotals := map[string][]int{}
	for _, r := range results {
		var comps []string
		json.Unmarshal(r.CompetenciesAssessed, &comps)
		for _, c := range comps {
			compTotals[c] = append(compTotals[c], r.ProficiencyScore)
		}
	}

	final := map[string]float64{}
	for code, scores := range compTotals {
		sum := 0
		for _, v := range scores {
			sum += v
		}
		final[code] = float64(sum) / float64(len(scores))
	}
	return final
}

func (s *AssessmentService) getAssessmentProficiencyStats(assessmentID string) (float64, int) {
	var stats struct {
		Avg   float64
		Count int
	}
	db.DB.Model(&models.Response{}).Where("assessmentId = ? AND proficiencyScore > 0", assessmentID).
		Select("AVG(proficiencyScore) as avg, COUNT(*) as count").Scan(&stats)

	if stats.Count == 0 {
		return 2.0, 0
	}
	return stats.Avg, stats.Count
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
		IsCheckpoint:  scenario.IsCheckpoint,
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
