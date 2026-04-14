package main

import (
	"war-room-backend/internal/config"
	"war-room-backend/internal/db"
	"war-room-backend/internal/handlers"
	"war-room-backend/internal/services"

	echojwt "github.com/labstack/echo-jwt/v4"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
)

func main() {
	// Load Config
	cfg := config.LoadConfig()

	// Connect to Database
	db.Connect(cfg)

	// Data Manager
	dataManager := services.NewDataManager()

	// Services
	authService := services.NewAuthService(cfg)
	assessmentService := services.NewAssessmentService(dataManager)
	batchService := services.NewBatchService()
	aiService := services.NewAIService()
	demoService := services.NewDemoService(aiService)

	// Handlers
	authHandler := handlers.NewAuthHandler(authService)
	assessmentHandler := handlers.NewAssessmentHandler(assessmentService)
	configHandler := handlers.NewConfigHandler(dataManager)
	batchHandler := handlers.NewBatchHandler(batchService)
	demoHandler := handlers.NewDemoHandler(demoService)

	// Initialize Echo
	e := echo.New()

	// Middleware
	e.Use(middleware.Logger())
	e.Use(middleware.Recover())
	e.Use(middleware.CORS())

	// Routes
	e.GET("/health", handlers.HealthCheck)

	// API Group
	api := e.Group("/api")
	api.GET("/health", handlers.HealthCheck)

	// Config Routes (public)
	api.GET("/config/mentors", configHandler.GetMentors)
	api.GET("/config/investors", configHandler.GetInvestors)
	api.GET("/config/leaders", configHandler.GetLeaders)
	api.GET("/config/competencies", configHandler.GetCompetencies)
	api.GET("/config/stages", configHandler.GetStages)
	api.GET("/config/stage-weights", configHandler.GetStageWeights)

	// Batch validation (public — needed before login)
	api.POST("/batches/validate", batchHandler.ValidateCode)

	// WebSocket leaderboard (public — participants connect with batchCode)
	api.GET("/batches/:code/live", batchHandler.LiveLeaderboard)

	// Auth Routes
	authGroup := api.Group("/auth")
	authGroup.POST("/register", authHandler.Register)
	authGroup.POST("/login", authHandler.Login)

	// Demo Routes (public — no auth for demo experience)
	demo := api.Group("/demo")
	demo.POST("/generate-scenario", demoHandler.GenerateScenario)
	demo.POST("/generate-followup", demoHandler.GenerateFollowup)
	demo.POST("/generate-pitch", demoHandler.GeneratePitch)
	demo.POST("/generate-pitch-qna", demoHandler.GeneratePitchQnA)
	demo.POST("/generate-negotiation", demoHandler.GenerateNegotiation)
	demo.POST("/generate-competency-report", demoHandler.GenerateCompetencyReport)
	demo.POST("/evaluate-response", demoHandler.EvaluateResponse)
	demo.POST("/evaluate-voice", demoHandler.EvaluateVoice)

	// Protected Routes
	protected := api.Group("")
	protected.Use(echojwt.WithConfig(echojwt.Config{
		SigningKey: []byte(cfg.JWTSecret),
	}))

	// Admin — batch management (protected + admin-only)
	admin := protected.Group("/admin")
	admin.Use(handlers.AdminOnly)
	admin.POST("/batches", batchHandler.CreateBatch)
	admin.GET("/batches", batchHandler.ListBatches)
	admin.GET("/batches/:id", batchHandler.GetBatchDetail)
	admin.PATCH("/batches/:id", batchHandler.UpdateBatch)
	admin.DELETE("/batches/:id", batchHandler.DeleteBatch)
	admin.GET("/batches/:id/participants", batchHandler.GetBatchParticipants)
	admin.GET("/batches/:id/stats", batchHandler.GetBatchStats)

	// Leaderboard (authenticated)
	protected.GET("/batches/:code/leaderboard", batchHandler.GetLeaderboard)

	// Assessment CRUD
	protected.POST("/assessments", assessmentHandler.Create)
	protected.GET("/assessments", assessmentHandler.List)
	protected.GET("/assessments/:id", assessmentHandler.Get)

	// Legacy per-question submit (kept for backward compat)
	protected.POST("/assessments/:id/responses", assessmentHandler.SubmitResponse)
	protected.POST("/assessments/:id/stage-responses", assessmentHandler.SubmitStageResponses)

	// NEW: Phase-level submit (v2 flow)
	protected.POST("/assessments/:id/phase-submit", assessmentHandler.SubmitPhase)

	// Character selection
	protected.GET("/assessments/:id/characters", assessmentHandler.GetCharacters)
	protected.POST("/assessments/:id/characters", assessmentHandler.SetCharacters)

	// Phase scenario (between stages)
	protected.POST("/assessments/:id/phase-scenario", assessmentHandler.AnswerPhaseScenario)

	// AI end-of-phase challenge question
	protected.POST("/assessments/:id/generate-ai-question", assessmentHandler.GenerateAiQuestion)

	// Mentor Lifeline
	protected.POST("/assessments/:id/mentor", assessmentHandler.UseMentorLifeline)

	// War Room
	protected.POST("/assessments/:id/warroom/pitch", assessmentHandler.SubmitPitch)
	protected.POST("/assessments/:id/warroom/respond", assessmentHandler.RespondToInvestor)
	protected.GET("/assessments/:id/warroom/scorecard", assessmentHandler.GetScorecard)
	protected.GET("/assessments/:id/warroom/offers", assessmentHandler.GetWarRoomOffers)
	protected.POST("/assessments/:id/warroom/counter", assessmentHandler.CounterNegotiate)
	protected.POST("/assessments/:id/warroom/counter-audio", assessmentHandler.CounterNegotiateAudio)
	protected.POST("/assessments/:id/warroom/accept-deal", assessmentHandler.AcceptDeal)
	protected.POST("/assessments/:id/warroom/reject-offer", assessmentHandler.RejectOffer)
	protected.POST("/assessments/:id/warroom/pitch-audio", assessmentHandler.SubmitPitchAudio)
	protected.POST("/assessments/:id/warroom/respond-audio", assessmentHandler.RespondToInvestorAudio)

	// Report
	protected.GET("/assessments/:id/report", assessmentHandler.GetReport)

	// Dynamic Scenarios
	protected.GET("/assessments/:id/dynamic-scenario", assessmentHandler.GetDynamicScenario)
	protected.GET("/assessments/:id/stage/:stageId/dynamic-scenarios", assessmentHandler.GetStageDynamicScenarios)
	protected.POST("/assessments/:id/dynamic-scenario/submit", assessmentHandler.SubmitDynamicScenario)

	// Flow Branching
	protected.POST("/assessments/:id/restart", assessmentHandler.Restart)
	protected.POST("/assessments/:id/buyout", assessmentHandler.HandleBuyout)
	protected.POST("/assessments/:id/walkout", assessmentHandler.HandleWalkout)

	// Auth info
	protected.GET("/auth/me", authHandler.Me)

	// Start Server
	e.Logger.Fatal(e.Start(":" + cfg.Port))
}
