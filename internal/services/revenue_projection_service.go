package services

import (
	"math"
	"war-room-backend/internal/models"
)

// RevenueProjectionService computes a simulated projected ARR for the leaderboard.
// The projection is personalized based on each user's competency scores and
// average proficiency across all their responses.
type RevenueProjectionService struct{}

func NewRevenueProjectionService() *RevenueProjectionService {
	return &RevenueProjectionService{}
}

// ComputeRevenueProjection returns a projected ARR (in currency units as int64).
//
// stageIndex: actual StageNumber of the active phase
// allCompScores: map of competency code → weighted average score (1.0–3.0) across ALL completed stages
// stageStats: map of stage number -> proficiency stats for that stage
func (s *RevenueProjectionService) ComputeRevenueProjection(
	stageIndex int,
	allCompScores map[string]float64,
	stageStats map[int]models.StageProficiencyStat,
) int64 {
	// Start with baseline revenue
	arr := 100000.0

	// Define the logical order of stages
	stageSequence := []int{-2, -1, 0, 1, 2, 3, 5, 4}

	// Calculate compounded ARR stage-by-stage up to current stage
	passedStage0 := false
	for _, sNum := range stageSequence {
		if sNum > stageIndex {
			break
		}

		stat, exists := stageStats[sNum]
		if !exists || stat.Count == 0 {
			arr *= 0.9 // Penalty for skipping/inaction
		} else {
			if stat.Avg >= 2.5 {
				arr *= 2.0 // Strong growth for good plan/strategy
			} else if stat.Avg >= 1.5 {
				arr *= 1.3 // Steady growth for neutral decisions
			} else {
				arr *= 0.8 // Loss of revenue for wrong decisions
			}
		}

		if sNum == 0 {
			passedStage0 = true
		}
	}

	// Revenue should only start compounding past 100k after Phase 0 (Commitment)
	if !passedStage0 || stageIndex < 0 {
		return 0
	}

	// Overall competency multiplier from ALL 8 competencies.
	// Each competency is scored 1–3. Average across those that exist.
	compSum := 0.0
	compCount := 0
	for _, code := range []string{"C1", "C2", "C3", "C4", "C5", "C6", "C7", "C8"} {
		if score, ok := allCompScores[code]; ok && score > 0 {
			compSum += score
			compCount++
		}
	}
	avgComp := 2.0 // neutral baseline
	if compCount > 0 {
		avgComp = compSum / float64(compCount)
	}

	// Map avgComp (1.0–3.0) to a multiplier range (0.4–1.6).
	competencyMul := 0.4 + (avgComp-1.0)*0.6

	projected := arr * competencyMul

	// Cap at a reasonable simulation ceiling
	projected = math.Min(projected, 500_000_000) // ₹50Cr cap

	return int64(math.Round(projected))
}
