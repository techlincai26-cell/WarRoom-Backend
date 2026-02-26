package services

import (
	"encoding/json"
	"log"
	"war-room-backend/internal/data"
	"war-room-backend/internal/models"
)

// ============================================
// DATA MANAGER - Loads & serves simulation config
// ============================================

type DataManager struct {
	Config           *models.SimulationConfig
	StageMap         map[string]models.SimStage
	QuestionMap      map[string]models.SimQuestion
	QuestionStageMap map[string]string // questionId -> stageId
	StageOrder       []string          // ordered stage IDs
	MentorMap        map[string]models.Mentor
	InvestorMap      map[string]models.Investor
	LeaderMap        map[string]models.Leader
	CompetencyMap    map[string]models.Competency
}

func NewDataManager() *DataManager {
	dm := &DataManager{
		StageMap:         make(map[string]models.SimStage),
		QuestionMap:      make(map[string]models.SimQuestion),
		QuestionStageMap: make(map[string]string),
		StageOrder:       make([]string, 0),
		MentorMap:        make(map[string]models.Mentor),
		InvestorMap:      make(map[string]models.Investor),
		LeaderMap:        make(map[string]models.Leader),
		CompetencyMap:    make(map[string]models.Competency),
	}
	dm.loadData()
	return dm
}

func (dm *DataManager) loadData() {
	bytes := data.GetSimulationData()
	if len(bytes) == 0 {
		log.Fatal("Simulation data is empty")
	}

	var config models.SimulationConfig
	err := json.Unmarshal(bytes, &config)
	if err != nil {
		log.Fatal("Failed to parse simulation data: ", err)
	}
	dm.Config = &config

	// Index stages and questions
	for _, stage := range config.Stages {
		dm.StageMap[stage.ID] = stage
		dm.StageOrder = append(dm.StageOrder, stage.ID)
		for _, q := range stage.Questions {
			dm.QuestionMap[q.QID] = q
			dm.QuestionStageMap[q.QID] = stage.ID
		}
	}

	// Index mentors
	for _, m := range config.Mentors {
		dm.MentorMap[m.ID] = m
	}

	// Index investors
	for _, inv := range config.Investors {
		dm.InvestorMap[inv.ID] = inv
	}

	// Index leaders
	for _, l := range config.Leaders {
		dm.LeaderMap[l.ID] = l
	}

	// Index competencies
	for _, c := range config.Competencies {
		dm.CompetencyMap[c.Code] = c
	}

	log.Printf("[DataManager] Loaded: %d stages, %d questions, %d mentors, %d investors, %d leaders, %d competencies",
		len(dm.StageMap), len(dm.QuestionMap), len(dm.MentorMap), len(dm.InvestorMap), len(dm.LeaderMap), len(dm.CompetencyMap))
}

// GetStage returns stage data by ID
func (dm *DataManager) GetStage(stageID string) *models.SimStage {
	stage, ok := dm.StageMap[stageID]
	if !ok {
		return nil
	}
	return &stage
}

// GetQuestion returns question data by ID
func (dm *DataManager) GetQuestion(questionID string) *models.SimQuestion {
	q, ok := dm.QuestionMap[questionID]
	if !ok {
		return nil
	}
	return &q
}

// GetFirstQuestionInStage returns the first question ID for a stage
func (dm *DataManager) GetFirstQuestionInStage(stageID string) string {
	stage, ok := dm.StageMap[stageID]
	if !ok || len(stage.Questions) == 0 {
		return ""
	}
	return stage.Questions[0].QID
}

// GetNextQuestionID returns the next question in the sequence
func (dm *DataManager) GetNextQuestionID(currentQID string, selectedOptionID string) string {
	q, ok := dm.QuestionMap[currentQID]
	if !ok {
		return ""
	}

	// 1. Check if selected option has a specific next
	for _, opt := range q.Options {
		if opt.ID == selectedOptionID && opt.Next != "" {
			return opt.Next
		}
	}

	// 2. Check if question itself has a defined next
	if q.Next != "" {
		return q.Next
	}

	// 3. Fallback: next question in the stage sequence
	stageID := dm.QuestionStageMap[currentQID]
	stage, ok := dm.StageMap[stageID]
	if !ok {
		return ""
	}

	for i, sq := range stage.Questions {
		if sq.QID == currentQID && i+1 < len(stage.Questions) {
			return stage.Questions[i+1].QID
		}
	}

	return "" // No more questions in this stage
}

// GetNextStageID returns the next stage in the simulation flow
func (dm *DataManager) GetNextStageID(currentStageID string) string {
	for i, sid := range dm.StageOrder {
		if sid == currentStageID && i+1 < len(dm.StageOrder) {
			return dm.StageOrder[i+1]
		}
	}
	return "" // No more stages
}

// IsLastQuestionInStage checks if the current question is the last one in its stage
func (dm *DataManager) IsLastQuestionInStage(questionID string) bool {
	stageID := dm.QuestionStageMap[questionID]
	stage, ok := dm.StageMap[stageID]
	if !ok || len(stage.Questions) == 0 {
		return true
	}
	return stage.Questions[len(stage.Questions)-1].QID == questionID
}

// GetStageWeights returns the competency weights for a given stage
func (dm *DataManager) GetStageWeights(stageID string) map[string]int {
	if dm.Config != nil && dm.Config.StageWeights != nil {
		if w, ok := dm.Config.StageWeights[stageID]; ok {
			return w
		}
	}
	return nil
}

// GetCompetencyDefs returns simplified competency definitions for AI prompts
func (dm *DataManager) GetCompetencyDefs() map[string]CompetencyDef {
	defs := make(map[string]CompetencyDef)
	for _, c := range dm.Config.Competencies {
		defs[c.Code] = CompetencyDef{
			Name:       c.Name,
			Developing: c.Developing,
			Strong:     c.Strong,
			Advanced:   c.Advanced,
		}
	}
	return defs
}

// GetMentors returns all available mentors
func (dm *DataManager) GetMentors() []models.Mentor {
	return dm.Config.Mentors
}

// GetInvestors returns all available investors
func (dm *DataManager) GetInvestors() []models.Investor {
	return dm.Config.Investors
}

// GetLeaders returns all available leaders
func (dm *DataManager) GetLeaders() []models.Leader {
	return dm.Config.Leaders
}
