package services

// latest_responses.go — utilities for tracking the user's latest answer per question
// and serialising it for downstream consumers (dynamic scenarios, reports, restart-with-continue).
//
// Design notes:
//   - LatestResponses is a map keyed by questionID. Re-answering the same question
//     overwrites the prior entry, so callers always see the user's current intent.
//   - We persist this on assessment.LatestResponses in addition to the per-Response
//     rows so that the assessment row remains a self-contained snapshot. This lets
//     the report endpoint be called at any phase without joining the responses table.
//   - PreviousResponses (chronological, capped) stays the AI-context summary used
//     by GenerateDynamicScenario; latest_responses is the source-of-truth for
//     "what did the user pick most recently for question X".

import (
	"encoding/json"
	"log"
	"sync"
	"time"

	"war-room-backend/internal/db"
	"war-room-backend/internal/models"
)

// LatestResponseEntry is a single per-question record inside Assessment.LatestResponses.
type LatestResponseEntry struct {
	Response    json.RawMessage `json:"response"`
	Proficiency int             `json:"proficiency"`
	StageID     string          `json:"stageId"`
	AnsweredAt  time.Time       `json:"answeredAt"`
}

// latestResponseLocks guards concurrent writes to the LatestResponses map for the
// same assessment. SubmitResponse, SubmitPhase, and SubmitDynamicScenario can all
// race when a user submits quickly; we serialise writes per assessment so we don't
// drop updates from a read-modify-write cycle on json.RawMessage.
var latestResponseLocks sync.Map // assessmentID -> *sync.Mutex

func lockForAssessment(assessmentID string) *sync.Mutex {
	v, _ := latestResponseLocks.LoadOrStore(assessmentID, &sync.Mutex{})
	return v.(*sync.Mutex)
}

// upsertLatestResponse writes a single questionID -> entry into Assessment.LatestResponses.
// It is safe to call from goroutines and from concurrent request handlers.
func upsertLatestResponse(assessmentID, stageID, questionID string, response json.RawMessage, proficiency int) {
	if questionID == "" {
		return
	}
	mu := lockForAssessment(assessmentID)
	mu.Lock()
	defer mu.Unlock()

	var assessment models.Assessment
	if err := db.DB.Where("id = ?", assessmentID).First(&assessment).Error; err != nil {
		return
	}

	latest := map[string]LatestResponseEntry{}
	if len(assessment.LatestResponses) > 0 {
		if err := json.Unmarshal(assessment.LatestResponses, &latest); err != nil {
			// Corrupt JSON — log and restart the map rather than swallow future writes.
			log.Printf("[LatestResponses] corrupt JSON for assessment %s, resetting: %v", assessmentID, err)
			latest = map[string]LatestResponseEntry{}
		}
	}

	// Deep-copy the raw response so the caller's buffer can't mutate it later.
	respCopy := make(json.RawMessage, len(response))
	copy(respCopy, response)

	latest[questionID] = LatestResponseEntry{
		Response:    respCopy,
		Proficiency: proficiency,
		StageID:     stageID,
		AnsweredAt:  time.Now(),
	}

	encoded, err := json.Marshal(latest)
	if err != nil {
		log.Printf("[LatestResponses] failed to marshal for assessment %s: %v", assessmentID, err)
		return
	}

	// Use Update on the column directly to avoid clobbering concurrent writes
	// to other Assessment fields (e.g. revenue projection being updated elsewhere).
	if err := db.DB.Model(&models.Assessment{}).
		Where("id = ?", assessmentID).
		Update("latest_responses", encoded).Error; err != nil {
		log.Printf("[LatestResponses] failed to persist for assessment %s: %v", assessmentID, err)
	}
}

// getLatestResponses returns the unmarshalled latest-response map. Never returns nil.
func getLatestResponses(assessmentID string) map[string]LatestResponseEntry {
	out := map[string]LatestResponseEntry{}
	var assessment models.Assessment
	if err := db.DB.Where("id = ?", assessmentID).First(&assessment).Error; err != nil {
		return out
	}
	if len(assessment.LatestResponses) == 0 {
		return out
	}
	if err := json.Unmarshal(assessment.LatestResponses, &out); err != nil {
		return map[string]LatestResponseEntry{}
	}
	return out
}

// buildContextForDynamicScenario returns a string summarising the user's most recent
// picks for AI prompt consumption. Prefers LatestResponses (per-question current
// state); falls back to PreviousResponses (chronological) when empty.
func (s *AssessmentService) buildContextForDynamicScenario(assessment *models.Assessment) string {
	if len(assessment.LatestResponses) > 0 {
		var latest map[string]LatestResponseEntry
		if err := json.Unmarshal(assessment.LatestResponses, &latest); err == nil && len(latest) > 0 {
			return s.formatLatestResponses(latest)
		}
	}
	return string(assessment.PreviousResponses)
}

// formatLatestResponses converts the latest-responses map into the
// "[{q,a,prof}]" JSON shape that AI prompts already consume — keeps the existing
// prompt template stable while letting us swap the underlying data source.
func (s *AssessmentService) formatLatestResponses(latest map[string]LatestResponseEntry) string {
	type promptEntry struct {
		Q    string `json:"q"`
		A    string `json:"a"`
		Prof int    `json:"prof"`
	}

	entries := make([]promptEntry, 0, len(latest))
	for qID, entry := range latest {
		q := s.DataManager.GetQuestion(qID)
		questionText := qID
		if q != nil {
			questionText = q.Text
		}

		// Resolve the human-readable answer (option text for MCQ, raw text otherwise).
		var respMap map[string]interface{}
		_ = json.Unmarshal(entry.Response, &respMap)
		answerText := ""
		if text, ok := respMap["text"].(string); ok && text != "" {
			answerText = text
		} else if optID, ok := respMap["selectedOptionId"].(string); ok && q != nil {
			for _, opt := range q.Options {
				if opt.ID == optID {
					answerText = opt.Text
					break
				}
			}
			if answerText == "" {
				answerText = optID
			}
		}

		entries = append(entries, promptEntry{Q: questionText, A: answerText, Prof: entry.Proficiency})
	}

	encoded, err := json.Marshal(entries)
	if err != nil {
		return ""
	}
	return string(encoded)
}
