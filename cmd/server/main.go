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

	// Handlers
	authHandler := handlers.NewAuthHandler(authService)
	assessmentHandler := handlers.NewAssessmentHandler(assessmentService)
	configHandler := handlers.NewConfigHandler(dataManager)

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

	// Auth Routes
	authGroup := api.Group("/auth")
	authGroup.POST("/register", authHandler.Register)
	authGroup.POST("/login", authHandler.Login)

	// Protected Routes
	protected := api.Group("")
	protected.Use(echojwt.WithConfig(echojwt.Config{
		SigningKey: []byte(cfg.JWTSecret),
	}))

	// Assessment CRUD
	protected.POST("/assessments", assessmentHandler.Create)
	protected.GET("/assessments", assessmentHandler.List)
	protected.GET("/assessments/:id", assessmentHandler.Get)
	protected.POST("/assessments/:id/responses", assessmentHandler.SubmitResponse)
	protected.POST("/assessments/:id/stage-responses", assessmentHandler.SubmitStageResponses)

	// Mentor Lifeline
	protected.POST("/assessments/:id/mentor", assessmentHandler.UseMentorLifeline)

	// War Room
	protected.POST("/assessments/:id/warroom/pitch", assessmentHandler.SubmitPitch)
	protected.POST("/assessments/:id/warroom/respond", assessmentHandler.RespondToInvestor)
	protected.GET("/assessments/:id/warroom/scorecard", assessmentHandler.GetScorecard)

	// Report
	protected.GET("/assessments/:id/report", assessmentHandler.GetReport)

	// Auth info
	protected.GET("/auth/me", authHandler.Me)

	// Start Server
	e.Logger.Fatal(e.Start(":" + cfg.Port))
}
