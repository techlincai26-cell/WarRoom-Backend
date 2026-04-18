package services

import (
	"strings"
	"sync"
	"time"
	"war-room-backend/internal/db"
	"war-room-backend/internal/models"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// BatchService handles batch creation and validation.
type BatchService struct{}

func NewBatchService() *BatchService {
	return &BatchService{}
}

// ValidateCode checks that a batch code exists and is active.
// Returns the batch on success or (nil, error) on failure.
func (s *BatchService) ValidateCode(code string) (*models.Batch, error) {
	normalized := strings.ToUpper(strings.TrimSpace(code))
	var batch models.Batch
	if err := db.DB.Where("code = ? AND active = ?", normalized, true).First(&batch).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, gorm.ErrRecordNotFound
		}
		return nil, err
	}
	return &batch, nil
}

// CreateBatch creates a new batch with the given details.
func (s *BatchService) CreateBatch(code, name string, level int, adminID string, startsAt, endsAt *time.Time) (*models.Batch, error) {
	normalized := strings.ToUpper(strings.TrimSpace(code))
	batch := &models.Batch{
		ID:       uuid.New().String(),
		Code:     normalized,
		Name:     name,
		Level:    level,
		AdminID:  adminID,
		Active:   true,
		StartsAt: startsAt,
		EndsAt:   endsAt,
	}
	if err := db.DB.Create(batch).Error; err != nil {
		return nil, err
	}
	return batch, nil
}

// ============================================
// ADMIN CRUD
// ============================================

// BatchWithCount is a Batch with an additional participant count.
type BatchWithCount struct {
	models.Batch
	ParticipantCount int64 `json:"participantCount"`
}

// ListBatches returns all batches with participant counts.
func (s *BatchService) ListBatches() ([]BatchWithCount, error) {
	var batches []models.Batch
	if err := db.DB.Order("created_at DESC").Find(&batches).Error; err != nil {
		return nil, err
	}

	if len(batches) == 0 {
		return []BatchWithCount{}, nil
	}

	// Optimized: Get all counts in one go
	type batchCount struct {
		BatchCode string `gorm:"column:batch_code"`
		Count     int64  `gorm:"column:count"`
	}
	var counts []batchCount
	batchCodes := make([]string, len(batches))
	for i, b := range batches {
		batchCodes[i] = b.Code
	}

	db.DB.Model(&models.User{}).
		Select("batch_code, count(*) as count").
		Where("batch_code IN ? AND role = ?", batchCodes, "participant").
		Group("batch_code").
		Scan(&counts)

	countMap := make(map[string]int64)
	for _, c := range counts {
		countMap[c.BatchCode] = c.Count
	}

	result := make([]BatchWithCount, len(batches))
	for i, b := range batches {
		result[i] = BatchWithCount{Batch: b, ParticipantCount: countMap[b.Code]}
	}
	return result, nil
}

// GetBatch returns a single batch by ID.
func (s *BatchService) GetBatch(id string) (*models.Batch, error) {
	var batch models.Batch
	if err := db.DB.First(&batch, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &batch, nil
}

// UpdateBatchInput holds optional fields for updating a batch.
type UpdateBatchInput struct {
	Name     *string    `json:"name"`
	Level    *int       `json:"level"`
	Active   *bool      `json:"active"`
	StartsAt *time.Time `json:"startsAt"`
	EndsAt   *time.Time `json:"endsAt"`
}

// UpdateBatch patches mutable fields on a batch.
func (s *BatchService) UpdateBatch(id string, input UpdateBatchInput) (*models.Batch, error) {
	var batch models.Batch
	if err := db.DB.First(&batch, "id = ?", id).Error; err != nil {
		return nil, err
	}
	updates := map[string]interface{}{}
	if input.Name != nil {
		updates["name"] = *input.Name
	}
	if input.Level != nil {
		updates["level"] = *input.Level
	}
	if input.Active != nil {
		updates["active"] = *input.Active
	}
	if input.StartsAt != nil {
		updates["starts_at"] = *input.StartsAt
	}
	if input.EndsAt != nil {
		updates["ends_at"] = *input.EndsAt
	}
	if len(updates) > 0 {
		if err := db.DB.Model(&batch).Updates(updates).Error; err != nil {
			return nil, err
		}
	}
	// Reload
	db.DB.First(&batch, "id = ?", id)
	return &batch, nil
}

// DeleteBatch permanently deletes a batch.
func (s *BatchService) DeleteBatch(id string) error {
	return db.DB.Delete(&models.Batch{}, "id = ?", id).Error
}

// ============================================
// PARTICIPANTS & STATS
// ============================================

// BatchParticipantDTO represents a participant in a batch with their assessment status.
type BatchParticipantDTO struct {
	UserID            string     `json:"userId"`
	UserName          string     `json:"userName"`
	Email             string     `json:"email"`
	JoinedAt          time.Time  `json:"joinedAt"`
	AssessmentID      *string    `json:"assessmentId"`
	Status            *string    `json:"status"`
	CurrentStage      *string    `json:"currentStage"`
	RevenueProjection *int64     `json:"revenueProjection"`
	StartedAt         *time.Time `json:"startedAt"`
	CompletedAt       *time.Time `json:"completedAt"`
}

// GetBatchParticipants returns all participant users in a batch along with their latest assessment info.
func (s *BatchService) GetBatchParticipants(batchCode string) ([]BatchParticipantDTO, error) {
	code := strings.ToUpper(strings.TrimSpace(batchCode))

	var users []models.User
	if err := db.DB.Where("batch_code = ? AND role = ?", code, "participant").Order("createdAt ASC").Find(&users).Error; err != nil {
		return nil, err
	}

	if len(users) == 0 {
		return []BatchParticipantDTO{}, nil
	}

	// Optimized: Get all latest assessments for these users in one go
	userIDs := make([]string, len(users))
	for i, u := range users {
		userIDs[i] = u.ID
	}

	var assessments []models.Assessment
	// Optimized: Get all assessments for these users in one go.
	// We'll map them by UserID, where the last one seen (latest createdAt) will be preserved.
	if err := db.DB.Where("userId IN ? AND batch_code = ?", userIDs, code).Order("createdAt ASC").Find(&assessments).Error; err != nil {
	}

	assessmentMap := make(map[string]models.Assessment)
	for _, a := range assessments {
		// Since they are ordered by createdAt ASC by default in Find,
		// the last one seen for a user will be the latest.
		assessmentMap[a.UserID] = a
	}

	result := make([]BatchParticipantDTO, 0, len(users))
	for _, u := range users {
		dto := BatchParticipantDTO{
			UserID:   u.ID,
			UserName: u.Name,
			Email:    u.Email,
			JoinedAt: u.CreatedAt,
		}

		if a, ok := assessmentMap[u.ID]; ok {
			dto.AssessmentID = &a.ID
			dto.Status = &a.Status
			dto.CurrentStage = &a.CurrentStage
			dto.RevenueProjection = &a.RevenueProjection
			dto.StartedAt = a.StartedAt
			dto.CompletedAt = a.CompletedAt
		}
		result = append(result, dto)
	}
	return result, nil
}

// BatchStatsDTO holds aggregate statistics for a batch.
type BatchStatsDTO struct {
	TotalParticipants int64   `json:"totalParticipants"`
	AssessmentsTotal  int64   `json:"assessmentsTotal"`
	InProgress        int64   `json:"inProgress"`
	Completed         int64   `json:"completed"`
	NotStarted        int64   `json:"notStarted"`
	AvgRevenue        float64 `json:"avgRevenue"`
	MaxRevenue        int64   `json:"maxRevenue"`
}

// GetBatchStats returns aggregate statistics for a batch.
func (s *BatchService) GetBatchStats(batchCode string) (*BatchStatsDTO, error) {
	code := strings.ToUpper(strings.TrimSpace(batchCode))

	stats := &BatchStatsDTO{}

	// Total participants
	db.DB.Model(&models.User{}).Where("batch_code = ? AND role = ?", code, "participant").Count(&stats.TotalParticipants)

	// Assessment counts
	db.DB.Model(&models.Assessment{}).Where("batch_code = ?", code).Count(&stats.AssessmentsTotal)
	db.DB.Model(&models.Assessment{}).Where("batch_code = ? AND status = ?", code, "IN_PROGRESS").Count(&stats.InProgress)
	db.DB.Model(&models.Assessment{}).Where("batch_code = ? AND status = ?", code, "COMPLETED").Count(&stats.Completed)
	db.DB.Model(&models.Assessment{}).Where("batch_code = ? AND status = ?", code, "NOT_STARTED").Count(&stats.NotStarted)

	// Revenue stats
	type revRow struct {
		Avg float64
		Max int64
	}
	var rev revRow
	db.DB.Model(&models.Assessment{}).
		Select("COALESCE(AVG(revenue_projection), 0) as avg, COALESCE(MAX(revenue_projection), 0) as max").
		Where("batch_code = ?", code).
		Scan(&rev)
	stats.AvgRevenue = rev.Avg
	stats.MaxRevenue = rev.Max

	return stats, nil
}

// ============================================
// LEADERBOARD
// ============================================

// GetLeaderboard returns all assessments in a batch ordered by revenue_projection descending.
type LeaderboardEntryDTO struct {
	Rank              int    `json:"rank"`
	UserID            string `json:"userId"`
	UserName          string `json:"name"`
	RevenueProjection int64  `json:"revenueProjection"`
	CurrentStage      string `json:"currentStage"`
	UserIdea          string `json:"userIdea"`
	Status            string `json:"status"`
}

var (
	leaderboardCache   = make(map[string]leaderboardCacheEntry)
	leaderboardCacheMu sync.Mutex
)

type leaderboardCacheEntry struct {
	Entries []LeaderboardEntryDTO
	Expiry  time.Time
}

func (s *BatchService) GetLeaderboard(batchCode string) ([]LeaderboardEntryDTO, error) {
	code := strings.ToUpper(strings.TrimSpace(batchCode))

	leaderboardCacheMu.Lock()
	if cache, ok := leaderboardCache[code]; ok && time.Now().Before(cache.Expiry) {
		leaderboardCacheMu.Unlock()
		return cache.Entries, nil
	}
	leaderboardCacheMu.Unlock()

	type row struct {
		UserID            string
		UserName          string
		RevenueProjection int64
		CurrentStage      string
		UserIdea          string
		Status            string
	}

	var rows []row
	err := db.DB.
		Table("assessments").
		Select("assessments.userId as user_id, users.name as user_name, assessments.revenue_projection, assessments.currentStage as current_stage, assessments.userIdea as user_idea, assessments.status").
		Joins("JOIN users ON users.id = assessments.userId").
		Joins("INNER JOIN (SELECT MAX(id) as latest_id FROM assessments WHERE batch_code = ? GROUP BY userId) as latest_assess ON assessments.id = latest_assess.latest_id", code).
		Where("assessments.batch_code = ?", code).
		Order("assessments.revenue_projection DESC").
		Scan(&rows).Error

	if err != nil {
		return nil, err
	}

	entries := make([]LeaderboardEntryDTO, len(rows))
	for i, r := range rows {
		entries[i] = LeaderboardEntryDTO{
			Rank:              i + 1,
			UserID:            r.UserID,
			UserName:          r.UserName,
			RevenueProjection: r.RevenueProjection,
			CurrentStage:      r.CurrentStage,
			UserIdea:          r.UserIdea,
			Status:            r.Status,
		}
	}

	leaderboardCacheMu.Lock()
	leaderboardCache[code] = leaderboardCacheEntry{
		Entries: entries,
		Expiry:  time.Now().Add(2 * time.Second), // Cache for 2 seconds
	}
	leaderboardCacheMu.Unlock()

	return entries, nil
}
