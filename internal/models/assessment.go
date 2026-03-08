package models

import (
	"encoding/json"
	"time"
)

// ============================================
// ASSESSMENT MODEL
// ============================================

type Assessment struct {
	ID     string `gorm:"primaryKey;type:varchar(191)" json:"id"`
	UserID string `gorm:"column:userId;index;not null;type:varchar(191)" json:"userId"`
	User   User   `json:"-" gorm:"foreignKey:UserID;references:ID"`

	Level         int    `gorm:"column:level;not null;default:1" json:"level"` // 1=Student, 2=Manager
	AttemptNumber int    `gorm:"column:attemptNumber;not null;default:1" json:"attemptNumber"`
	Status        string `gorm:"column:status;not null;default:'NOT_STARTED'" json:"status"`

	// Batch tracking
	BatchCode string `gorm:"column:batch_code;not null;default:''" json:"batchCode"`

	// Character selections (chosen before Stage 1)
	SelectedMentors   json.RawMessage `gorm:"column:selected_mentors;type:json" json:"selectedMentors"`     // ["tony_robbins","mel_robbins","grant_cardone"]
	SelectedLeaders   json.RawMessage `gorm:"column:selected_leaders;type:json" json:"selectedLeaders"`     // ["indira_nooyi","jack_ma","simon_sinek"]
	SelectedInvestors json.RawMessage `gorm:"column:selected_investors;type:json" json:"selectedInvestors"` // ["kevin_oleary","mark_cuban","barbara_corcoran"]

	// Stage tracking
	CurrentStage      string `gorm:"column:currentStage;not null;default:'STAGE_NEG2_IDEATION'" json:"currentStage"`
	CurrentQuestionID string `gorm:"column:currentQuestionId" json:"currentQuestionId"`
	SimulatedMonth    int    `gorm:"column:simulatedMonth;not null;default:0" json:"simulatedMonth"`

	// Business context
	BusinessContext json.RawMessage `gorm:"column:businessContext;type:json" json:"businessContext"`
	UserIdea        string          `gorm:"column:userIdea;type:text" json:"userIdea"`

	// Simulation state
	FinancialState json.RawMessage `gorm:"column:financialState;type:json" json:"financialState"`
	TeamState      json.RawMessage `gorm:"column:teamState;type:json" json:"teamState"`
	CustomerState  json.RawMessage `gorm:"column:customerState;type:json" json:"customerState"`
	ProductState   json.RawMessage `gorm:"column:productState;type:json" json:"productState"`
	MarketState    json.RawMessage `gorm:"column:marketState;type:json" json:"marketState"`

	// Revenue projection (live leaderboard value; updated after each phase submit)
	RevenueProjection int64 `gorm:"column:revenue_projection;not null;default:0" json:"revenueProjection"`

	// In-phase buffer: answers collected client-side are posted here before AI eval begins.
	// Cleared after each phase-submit completes.
	CurrentPhaseResponses json.RawMessage `gorm:"column:current_phase_responses;type:json" json:"currentPhaseResponses"`

	// Mentor lifelines
	MentorLifelinesRemaining int `gorm:"column:mentorLifelinesRemaining;not null;default:3" json:"mentorLifelinesRemaining"`

	// War Room
	WarRoomPitch string          `gorm:"column:warRoomPitch;type:text" json:"warRoomPitch"`
	DealResult   json.RawMessage `gorm:"column:dealResult;type:json" json:"dealResult"`

	// Timestamps
	StartedAt         *time.Time `gorm:"column:startedAt" json:"startedAt"`
	CompletedAt       *time.Time `gorm:"column:completedAt" json:"completedAt"`
	TotalDurationMins *int       `gorm:"column:totalDurationMinutes" json:"totalDurationMinutes"`
	LastActiveAt      *time.Time `gorm:"column:lastActiveAt" json:"lastActiveAt"`
	CreatedAt         time.Time  `gorm:"column:createdAt" json:"createdAt"`
	UpdatedAt         time.Time  `gorm:"column:updatedAt" json:"updatedAt"`
}

// ============================================
// STAGE MODEL
// ============================================

type Stage struct {
	ID            string          `gorm:"primaryKey;type:varchar(191)" json:"id"`
	AssessmentID  string          `gorm:"column:assessmentId;index;not null;type:varchar(191)" json:"assessmentId"`
	StageName     string          `gorm:"column:stageName;not null" json:"stageName"`
	StageNumber   int             `gorm:"column:stageNumber;not null" json:"stageNumber"`
	StartedAt     *time.Time      `gorm:"column:startedAt" json:"startedAt"`
	CompletedAt   *time.Time      `gorm:"column:completedAt" json:"completedAt"`
	DurationSecs  *int            `gorm:"column:durationSeconds" json:"durationSeconds"`
	QuestionsAns  int             `gorm:"column:questionsAnswered;not null;default:0" json:"questionsAnswered"`
	StateSnapshot json.RawMessage `gorm:"column:stateSnapshot;type:json" json:"stateSnapshot"`
	CreatedAt     time.Time       `gorm:"column:createdAt" json:"createdAt"`
	UpdatedAt     time.Time       `gorm:"column:updatedAt" json:"updatedAt"`
}

// ============================================
// RESPONSE MODEL
// ============================================

type Response struct {
	ID                   string          `gorm:"primaryKey;type:varchar(191)" json:"id"`
	AssessmentID         string          `gorm:"column:assessmentId;index;not null;type:varchar(191)" json:"assessmentId"`
	StageID              string          `gorm:"column:stageId;index;not null;type:varchar(191)" json:"stageId"`
	QuestionID           string          `gorm:"column:questionId;not null" json:"questionId"`
	QuestionType         string          `gorm:"column:questionType;not null" json:"questionType"`
	ResponseData         json.RawMessage `gorm:"column:responseData;type:json;not null" json:"responseData"`
	IsPending            bool            `gorm:"column:is_pending;not null;default:false" json:"isPending"` // true = collected but AI not yet run
	ProficiencyScore     *int            `gorm:"column:proficiencyScore" json:"proficiencyScore"`           // 1=P1, 2=P2, 3=P3
	CompetenciesAssessed json.RawMessage `gorm:"column:competenciesAssessed;type:json;not null" json:"competenciesAssessed"`
	StageWeights         json.RawMessage `gorm:"column:stageWeights;type:json" json:"stageWeights"`
	AIEvaluation         json.RawMessage `gorm:"column:aiEvaluation;type:json" json:"aiEvaluation"`
	ResponseTimeSecs     *int            `gorm:"column:responseTimeSeconds" json:"responseTimeSeconds"`
	StartedAt            time.Time       `gorm:"column:startedAt" json:"startedAt"`
	AnsweredAt           *time.Time      `gorm:"column:answeredAt" json:"answeredAt"`
	CreatedAt            time.Time       `gorm:"column:createdAt" json:"createdAt"`
}

// ============================================
// COMPETENCY SCORE MODEL
// ============================================

type CompetencyScore struct {
	ID              string          `gorm:"primaryKey;type:varchar(191)" json:"id"`
	AssessmentID    string          `gorm:"column:assessmentId;index;not null;type:varchar(191)" json:"assessmentId"`
	CompetencyCode  string          `gorm:"column:competencyCode;not null" json:"competencyCode"`
	CompetencyName  string          `gorm:"column:competencyName;not null" json:"competencyName"`
	StageScores     json.RawMessage `gorm:"column:stageScores;type:json;not null" json:"stageScores"`
	WeightedAverage float64         `gorm:"column:weightedAverage;not null;default:0" json:"weightedAverage"`
	Category        string          `gorm:"column:category;not null;default:'DEVELOPMENT_REQUIRED'" json:"category"`
	Evidence        json.RawMessage `gorm:"column:evidence;type:json" json:"evidence"`
	Strengths       json.RawMessage `gorm:"column:strengths;type:json" json:"strengths"`
	Weaknesses      json.RawMessage `gorm:"column:weaknesses;type:json" json:"weaknesses"`
	LastUpdated     time.Time       `gorm:"column:lastUpdated" json:"lastUpdated"`
}

// ============================================
// MENTOR INTERACTION MODEL
// ============================================

type MentorInteraction struct {
	ID              string    `gorm:"primaryKey;type:varchar(191)" json:"id"`
	AssessmentID    string    `gorm:"column:assessmentId;index;not null;type:varchar(191)" json:"assessmentId"`
	MentorID        string    `gorm:"column:mentorId;not null" json:"mentorId"`
	MentorName      string    `gorm:"column:mentorName;not null" json:"mentorName"`
	StageName       string    `gorm:"column:stageName;not null" json:"stageName"`
	QuestionContext string    `gorm:"column:questionContext;type:text" json:"questionContext"`
	GuidanceGiven   string    `gorm:"column:guidanceGiven;type:text" json:"guidanceGiven"`
	UsedAt          time.Time `gorm:"column:usedAt" json:"usedAt"`
}

// ============================================
// INVESTOR SCORECARD MODEL
// ============================================

type InvestorScorecard struct {
	ID               string          `gorm:"primaryKey;type:varchar(191)" json:"id"`
	AssessmentID     string          `gorm:"column:assessmentId;index;not null;type:varchar(191)" json:"assessmentId"`
	InvestorID       string          `gorm:"column:investorId;not null" json:"investorId"`
	InvestorName     string          `gorm:"column:investorName;not null" json:"investorName"`
	PrimaryScore     *int            `gorm:"column:primaryScore" json:"primaryScore"`
	BiasTraitScore   *int            `gorm:"column:biasTraitScore" json:"biasTraitScore"`
	BiasTraitName    string          `gorm:"column:biasTraitName" json:"biasTraitName"`
	RedFlag          bool            `gorm:"column:redFlag;not null;default:false" json:"redFlag"`
	RedFlagReasons   json.RawMessage `gorm:"column:redFlagReasons;type:json" json:"redFlagReasons"`
	DealDecision     string          `gorm:"column:dealDecision;not null;default:'WALK_OUT'" json:"dealDecision"`
	DealProposed     json.RawMessage `gorm:"column:dealProposed;type:json" json:"dealProposed"`
	DealAccepted     json.RawMessage `gorm:"column:dealAccepted;type:json" json:"dealAccepted"`
	Question         string          `gorm:"column:question;type:text" json:"question"`
	ParticipantResp  string          `gorm:"column:participantResponse;type:text" json:"participantResponse"`
	InvestorReaction string          `gorm:"column:investorReaction;type:text" json:"investorReaction"`
	CreatedAt        time.Time       `gorm:"column:createdAt" json:"createdAt"`
}

// ============================================
// REPORT MODEL
// ============================================

type Report struct {
	ID                 string          `gorm:"primaryKey;type:varchar(191)" json:"id"`
	AssessmentID       string          `gorm:"column:assessmentId;index;not null;type:varchar(191)" json:"assessmentId"`
	ReportType         string          `gorm:"column:reportType;not null;default:'FINAL'" json:"reportType"`
	DealSummary        json.RawMessage `gorm:"column:dealSummary;type:json" json:"dealSummary"`
	CompetencyRanking  json.RawMessage `gorm:"column:competencyRanking;type:json;not null" json:"competencyRanking"`
	SpiderChartData    json.RawMessage `gorm:"column:spiderChartData;type:json;not null" json:"spiderChartData"`
	ArchetypeNarrative string          `gorm:"column:archetypeNarrative;type:text" json:"archetypeNarrative"`
	EntrepreneurType   string          `gorm:"column:entrepreneurType" json:"entrepreneurType"`
	OrganizationalRole string          `gorm:"column:organizationalRole" json:"organizationalRole"`
	ActionPlan         json.RawMessage `gorm:"column:actionPlan;type:json" json:"actionPlan"`
	StageNarrations    json.RawMessage `gorm:"column:stageNarrations;type:json;not null" json:"stageNarrations"`
	RoleFitMap         json.RawMessage `gorm:"column:roleFitMap;type:json" json:"roleFitMap"`
	DetailedAnalysis   string          `gorm:"column:detailedAnalysis;type:text" json:"detailedAnalysis"`
	UserResponses      json.RawMessage `gorm:"column:userResponses;type:json" json:"userResponses"`
	GeneratedAt        time.Time       `gorm:"column:generatedAt" json:"generatedAt"`
}

// ============================================
// LEADERBOARD MODEL
// ============================================

type LeaderboardEntry struct {
	ID                string     `gorm:"primaryKey;type:varchar(191)" json:"id"`
	AssessmentID      string     `gorm:"column:assessmentId;not null;type:varchar(191)" json:"assessmentId"`
	UserID            string     `gorm:"column:userId;index;not null;type:varchar(191)" json:"userId"`
	UserName          string     `gorm:"column:userName" json:"userName"`
	DealMade          bool       `gorm:"column:dealMade;not null;default:false" json:"dealMade"`
	DealCapital       *float64   `gorm:"column:dealCapital" json:"dealCapital"`
	DealEquity        *float64   `gorm:"column:dealEquity" json:"dealEquity"`
	HighestInvScore   *int       `gorm:"column:highestInvestorScore" json:"highestInvestorScore"`
	CompetencyAverage *float64   `gorm:"column:competencyAverage" json:"competencyAverage"`
	EntrepreneurType  string     `gorm:"column:entrepreneurType" json:"entrepreneurType"`
	CompletedAt       *time.Time `gorm:"column:completedAt" json:"completedAt"`
	CreatedAt         time.Time  `gorm:"column:createdAt" json:"createdAt"`
}
