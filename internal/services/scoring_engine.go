package services

import (
	"encoding/json"
	"math"
	"strings"
	"war-room-backend/internal/models"
)

// ============================================
// SCORING ENGINE - SOP 2.0
// ============================================
// Formula: Σ(Stage_Score × Stage_Weight) ÷ Total_Weight = Avg Competency Score
// P1=1, P2=2, P3=3
// Categories:
//   2.55–3.0 = Advanced (P3)
//   1.85–2.54 = Strong (P2)
//   1.0–1.84 = Developing (P1)

const (
	ProficiencyMin      = 1.0
	ProficiencyMax      = 3.0
	CategoryStrongMin   = 1.85
	CategoryAdvancedMin = 2.55

	// Apply a mild neutral prior so early assessments do not look overly harsh.
	neutralBaselineScore = 2.10
	neutralPriorWeight   = 30.0
)

type ScoringEngine struct {
	Config *models.SimulationConfig
}

func NewScoringEngine(config *models.SimulationConfig) *ScoringEngine {
	return &ScoringEngine{Config: config}
}

// StageWeightMatrix defines the weight of each competency per stage
// Directly from the SOP 2.0 scoring rubric
var StageWeightMatrix = map[string]map[string]int{
	"STAGE_NEG2_IDEATION": {
		"C1": 40, "C2": 35, "C5": 15, "C8": 10,
	},
	"STAGE_NEG1_VISION": {
		"C2": 20, "C3": 10, "C5": 40, "C8": 30,
	},
	"STAGE_0_COMMITMENT": {
		"C3": 40, "C4": 40, "C5": 20,
	},
	"STAGE_1_VALIDATION": {
		"C1": 30, "C2": 30, "C4": 15, "C7": 25, "C9": 25,
	},
	"STAGE_2A_GROWTH": {
		"C3": 20, "C4": 35, "C5": 25, "C7": 20, "C9": 35,
	},
	"STAGE_2B_EXPANSION": {
		"C3": 20, "C4": 20, "C5": 25, "C6": 35,
	},
	"STAGE_3_SCALE": {
		"C2": 20, "C3": 20, "C7": 35, "C8": 25, "C9": 25,
	},
	"STAGE_WARROOM_PREP": {
		"C4": 30, "C5": 25, "C6": 30, "C8": 15,
	},
	"STAGE_4_WARROOM": {
		"C1": 12, "C2": 12, "C3": 13, "C4": 13,
		"C5": 13, "C6": 12, "C7": 13, "C8": 12,
	},
}

// CompetencyResult holds the calculated score for one competency
type CompetencyResult struct {
	Code            string         `json:"code"`
	Name            string         `json:"name"`
	WeightedAverage float64        `json:"weightedAverage"`
	Category        string         `json:"category"`
	StageScores     map[string]int `json:"stageScores"`  // stage -> P-score
	StageWeights    map[string]int `json:"stageWeights"` // stage -> weight
	Evidence        []EvidenceItem `json:"evidence"`
	Strengths       []string       `json:"strengths"`  // specific qualitative strengths
	Weaknesses      []string       `json:"weaknesses"` // specific qualitative weaknesses
	KeyInsight      string         `json:"keyInsight"` // AI-generated synthesis of why this score
}

type EvidenceItem struct {
	Stage       string          `json:"stage"`
	QuestionID  string          `json:"questionId"`
	Proficiency int             `json:"proficiency"`
	Response    string          `json:"response,omitempty"`
	AIEval      json.RawMessage `json:"aiEval,omitempty"` // Added for deeper analysis
}

// CalculateCompetencyScores calculates weighted average scores for all 9 competencies
func (se *ScoringEngine) CalculateCompetencyScores(
	stageScores map[string]map[string][]int, // stage -> competency -> list of P-scores
	evidence map[string]map[string][]EvidenceItem, // stage -> comp -> items
) map[string]*CompetencyResult {
	results := make(map[string]*CompetencyResult)

	// Initialize all 9 competencies
	competencyNames := map[string]string{
		"C1": "Problem Sensing",
		"C2": "Learning Agility",
		"C3": "Courage to Commit",
		"C4": "Financial Discipline",
		"C5": "Strategic Thinking",
		"C6": "Power & Influence",
		"C7": "Team & Customer Management",
		"C8": "Value Creation & Credibility",
		"C9": "Perseverance & Adaptability",
	}

	for code, name := range competencyNames {
		results[code] = &CompetencyResult{
			Code:         code,
			Name:         name,
			StageScores:  make(map[string]int),
			StageWeights: make(map[string]int),
			Strengths:    []string{},
			Weaknesses:   []string{},
			Evidence:     []EvidenceItem{},
		}
	}

	// For each stage, calculate the average P-score per competency
	for stageName, competencyMap := range stageScores {
		for compCode, pScores := range competencyMap {
			if len(pScores) == 0 {
				continue
			}
			// Average P-score for this competency in this stage
			sum := 0
			for _, s := range pScores {
				sum += s
			}
			avgPScore := int(math.Round(float64(sum) / float64(len(pScores))))

			if r, ok := results[compCode]; ok {
				r.StageScores[stageName] = avgPScore
				if w, wOk := StageWeightMatrix[stageName][compCode]; wOk {
					r.StageWeights[stageName] = w
				}
			}

			// Aggregate qualitative data from evidence
			if r, ok := results[compCode]; ok && evidence != nil {
				if stageEv, ok := evidence[stageName]; ok {
					if items, ok := stageEv[compCode]; ok {
						for _, item := range items {
							r.Evidence = append(r.Evidence, item)

							// Try to parse strengths/weaknesses from AIEval
							if len(item.AIEval) > 0 {
								var eval struct {
									Strengths  []string `json:"strengths"`
									Weaknesses []string `json:"weaknesses"`
									Signals    struct {
										Positive []string `json:"positive"`
										Negative []string `json:"negative"`
									} `json:"signals"`
								}
								if err := json.Unmarshal(item.AIEval, &eval); err == nil {
									r.Strengths = append(r.Strengths, eval.Strengths...)
									r.Strengths = append(r.Strengths, eval.Signals.Positive...)
									r.Weaknesses = append(r.Weaknesses, eval.Weaknesses...)
									r.Weaknesses = append(r.Weaknesses, eval.Signals.Negative...)
								}
							}
						}
					}
				}
			}
		}
	}

	// Calculate weighted average for each competency
	for _, result := range results {
		totalWeightedScore := 0.0
		totalWeight := 0.0

		for stageName, pScore := range result.StageScores {
			weight := 0
			if w, ok := StageWeightMatrix[stageName][result.Code]; ok {
				weight = w
			} else if w, ok := result.StageWeights[stageName]; ok {
				weight = w
			}
			if weight > 0 {
				totalWeightedScore += float64(pScore) * float64(weight)
				totalWeight += float64(weight)
			}
		}

		if totalWeight > 0 {
			rawAverage := totalWeightedScore / totalWeight
			blendedAverage := (rawAverage*totalWeight + neutralBaselineScore*neutralPriorWeight) / (totalWeight + neutralPriorWeight)
			result.WeightedAverage = math.Round(blendedAverage*100) / 100
			if result.WeightedAverage < ProficiencyMin {
				result.WeightedAverage = ProficiencyMin
			}
			if result.WeightedAverage > ProficiencyMax {
				result.WeightedAverage = ProficiencyMax
			}
		}
		result.Category = ClassifyCompetency(result.WeightedAverage)

		// Deduplicate and prune strengths/weaknesses
		result.Strengths = uniqueStrings(result.Strengths)
		result.Weaknesses = uniqueStrings(result.Weaknesses)

		// Simple logic for KeyInsight based on score and trends
		if result.WeightedAverage >= CategoryAdvancedMin {
			result.KeyInsight = "Exhibits exceptional mastery. Consistently makes high-impact decisions under pressure."
		} else if result.WeightedAverage >= CategoryStrongMin {
			result.KeyInsight = "Strong and reliable performance. Minor blind spots exist under extreme stress."
		} else {
			result.KeyInsight = "Requires focused development. Performance is inconsistent when dealing with high-stakes scenarios."
		}
	}

	return results
}

func uniqueStrings(input []string) []string {
	if len(input) == 0 {
		return []string{}
	}
	m := make(map[string]bool)
	list := []string{}
	for _, s := range input {
		s = strings.TrimSpace(s)
		if s == "" || m[s] {
			continue
		}
		m[s] = true
		list = append(list, s)
	}
	if len(list) > 3 {
		return list[:3] // Keep only top 3
	}
	return list
}

// ClassifyCompetency maps a weighted average score to a category
func ClassifyCompetency(avg float64) string {
	switch {
	case avg >= CategoryAdvancedMin:
		return "P3"
	case avg >= CategoryStrongMin:
		return "P2"
	default:
		return "P1"
	}
}

// RankCompetencies returns competencies sorted from highest to lowest score
func RankCompetencies(results map[string]*CompetencyResult) []*CompetencyResult {
	ranked := make([]*CompetencyResult, 0, len(results))
	for _, r := range results {
		ranked = append(ranked, r)
	}

	// Sort descending by WeightedAverage
	for i := 0; i < len(ranked); i++ {
		for j := i + 1; j < len(ranked); j++ {
			if ranked[j].WeightedAverage > ranked[i].WeightedAverage {
				ranked[i], ranked[j] = ranked[j], ranked[i]
			}
		}
	}
	return ranked
}

// ============================================
// ENTREPRENEUR TYPE CLASSIFICATION
// ============================================

type EntrepreneurProfile struct {
	Type           string `json:"type"`
	Description    string `json:"description"`
	Interpretation string `json:"interpretation"`
}

// ClassifyEntrepreneur determines the entrepreneur type based on competency scores
func ClassifyEntrepreneur(results map[string]*CompetencyResult) *EntrepreneurProfile {
	p3Count := 0
	p2Count := 0
	hasP1UnderStress := false

	for _, r := range results {
		if r.WeightedAverage >= CategoryAdvancedMin {
			p3Count++
		} else if r.WeightedAverage >= CategoryStrongMin {
			p2Count++
		} else {
			hasP1UnderStress = true
		}
	}

	allP2Min := true
	for _, r := range results {
		if r.WeightedAverage < CategoryStrongMin {
			allP2Min = false
			break
		}
	}

	switch {
	case allP2Min && p3Count >= 5 && !hasP1UnderStress:
		return &EntrepreneurProfile{
			Type:           "Natural Entrepreneur (Growth Ready)",
			Description:    "P2 minimum in all C1–C8, P3 in at least 5 competencies, No P1 under stress",
			Interpretation: "Can ideate, execute, scale, negotiate, protect culture and capital",
		}
	case allP2Min && p3Count >= 3:
		return &EntrepreneurProfile{
			Type:           "Strong Entrepreneur",
			Description:    "P2 minimum in all C1–C8, P3 in 3–4 areas, Minor temporary P1 under extreme stress",
			Interpretation: "Can build & scale with manageable blind spots",
		}
	case p2Count >= 5:
		return &EntrepreneurProfile{
			Type:           "Emerging Entrepreneur",
			Description:    "P2 in 5–6 areas, P1 in 1–2 areas under pressure",
			Interpretation: "Needs complementary co-founder or development",
		}
	default:
		// Check for Investor profile: strong C4, C5, C6
		c4 := results["C4"]
		c5 := results["C5"]
		c6 := results["C6"]
		if c4 != nil && c5 != nil && c6 != nil &&
			c4.WeightedAverage >= 2.3 && c5.WeightedAverage >= 2.3 && c6.WeightedAverage >= 2.3 {
			return &EntrepreneurProfile{
				Type:           "Investor - Capital Allocator",
				Description:    "Strong C4 Financial, C5 Strategic, C6 Negotiation",
				Interpretation: "PE / VC / Board roles",
			}
		}
		return &EntrepreneurProfile{
			Type:           "Emerging Entrepreneur",
			Description:    "Development needed across multiple competencies",
			Interpretation: "Needs structured support or mentorship program",
		}
	}
}

// ============================================
// ORGANIZATIONAL ROLE FIT
// ============================================

type RoleFit struct {
	Role                 string   `json:"role"`
	DominantCompetencies []string `json:"dominantCompetencies"`
	BestEnvironment      string   `json:"bestEnvironment"`
}

// DetermineRoleFit maps competency profile to organizational roles
func DetermineRoleFit(ranked []*CompetencyResult) *RoleFit {
	if len(ranked) < 3 {
		return &RoleFit{Role: "Undetermined"}
	}

	top3 := map[string]bool{}
	for i := 0; i < 3 && i < len(ranked); i++ {
		top3[ranked[i].Code] = true
	}

	// Role matching based on SOP 2.0 table
	switch {
	case top3["C5"] && top3["C4"] && (top3["C7"] || top3["C3"]):
		return &RoleFit{Role: "CEO", DominantCompetencies: []string{"C5", "C4", "C7", "C3"}, BestEnvironment: "Scaling companies"}
	case top3["C1"] && top3["C3"] && top3["C5"]:
		return &RoleFit{Role: "Founder", DominantCompetencies: []string{"C1", "C3", "C5"}, BestEnvironment: "Early-stage startups"}
	case top3["C4"] && top3["C5"]:
		return &RoleFit{Role: "Finance - CFO / Finance Strategist", DominantCompetencies: []string{"C4", "C5"}, BestEnvironment: "Structured growth firms"}
	case top3["C4"] && top3["C7"] && top3["C5"]:
		return &RoleFit{Role: "Operations - COO", DominantCompetencies: []string{"C4", "C7", "C5"}, BestEnvironment: "Growth & scaling phase"}
	case top3["C6"] && (top3["C5"] || top3["C3"]):
		return &RoleFit{Role: "Sales - Strategic Negotiator / Deal Maker", DominantCompetencies: []string{"C6", "C5", "C3"}, BestEnvironment: "Revenue-driven orgs"}
	case top3["C1"] && top3["C2"]:
		return &RoleFit{Role: "Product - Product Manager, R&D", DominantCompetencies: []string{"C1", "C2", "C5"}, BestEnvironment: "Innovation-led firms"}
	case top3["C1"] && top3["C7"]:
		return &RoleFit{Role: "Customer - Customer Experience", DominantCompetencies: []string{"C1", "C7"}, BestEnvironment: "Brand-first orgs"}
	case top3["C7"] && top3["C8"]:
		return &RoleFit{Role: "Employee - HR, Culture & People Management", DominantCompetencies: []string{"C7", "C8"}, BestEnvironment: "Mid-large orgs"}
	case top3["C3"] && top3["C7"] && top3["C8"]:
		return &RoleFit{Role: "Escalations - Crisis, Turnaround", DominantCompetencies: []string{"C3", "C7", "C8"}, BestEnvironment: "Distressed environments"}
	default:
		return &RoleFit{Role: "Founder", DominantCompetencies: []string{ranked[0].Code, ranked[1].Code, ranked[2].Code}, BestEnvironment: "Early-stage ventures"}
	}
}

// ============================================
// INVESTOR DEAL LOGIC
// ============================================

type DealDecisionResult struct {
	Decision          string  `json:"decision"` // PRIORITY_1, PRIORITY_2, WALK_OUT
	CapitalOffer      float64 `json:"capitalOffer,omitempty"`
	EquityAsk         float64 `json:"equityAsk,omitempty"`
	CapitalAcceptable float64 `json:"capitalAcceptable,omitempty"`
	EquityAcceptable  float64 `json:"equityAcceptable,omitempty"`
}

// CalculateDealDecision implements the SOP 2.0 investor deal formula
// IF (P ≥ 3) AND (B ≥ 3) AND (RedFlag = NO)
//
//	IF (P ≥ 4 AND B ≥ 4) → PRIORITY 1
//	ELSE → PRIORITY 2
//
// ELSE → WALK_OUT
func CalculateDealDecision(primaryScore int, biasTraitScore int, hasRedFlag bool, capitalAsked float64, equityOffered float64) *DealDecisionResult {
	if hasRedFlag || primaryScore < 3 || biasTraitScore < 3 {
		return &DealDecisionResult{Decision: "WALK_OUT"}
	}

	if primaryScore >= 4 && biasTraitScore >= 4 {
		// PRIORITY 1: Capital = 90% of ask, Equity = E + 20-30% (propose)
		// Acceptable: Capital = 100%, Equity = E + 2-19%
		return &DealDecisionResult{
			Decision:          "PRIORITY_1",
			CapitalOffer:      capitalAsked * 0.9,
			EquityAsk:         equityOffered + 25, // midpoint of 20-30%
			CapitalAcceptable: capitalAsked,
			EquityAcceptable:  equityOffered + 10, // midpoint of 2-19%
		}
	}

	// PRIORITY 2: Capital = 0.6C, Equity = E + 15-18%
	// Acceptable: Capital = 0.8C, Equity = E + 5-14%
	return &DealDecisionResult{
		Decision:          "PRIORITY_2",
		CapitalOffer:      capitalAsked * 0.6,
		EquityAsk:         equityOffered + 16, // midpoint of 15-18%
		CapitalAcceptable: capitalAsked * 0.8,
		EquityAcceptable:  equityOffered + 9, // midpoint of 5-14%
	}
}

// InvestorRedFlagTriggers maps each investor to their walk-out condition
var InvestorRedFlagTriggers = map[string]string{
	"kevin_oleary":     "financial_logic_weak",
	"mark_cuban":       "scalability_weak",
	"barbara_corcoran": "emotional_authenticity_weak",
	"lori_greiner":     "clarity_weak",
	"steven_bartlett":  "identity_unclear",
	"daymond_john":     "brand_weak",
	"robert_herjavec":  "trust_weak",
}

// RedFlagPenalties defines the behavior triggers that cause -1 penalty
var RedFlagPenalties = []string{
	"blames_others",
	"avoids_question",
	"overuse_hype_language",
	"defensive_aggressive_tone",
	"contradiction_detected",
}
