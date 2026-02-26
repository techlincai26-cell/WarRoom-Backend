package handlers

import (
	"net/http"
	"war-room-backend/internal/services"

	"github.com/labstack/echo/v4"
)

type ConfigHandler struct {
	DataManager *services.DataManager
}

func NewConfigHandler(dm *services.DataManager) *ConfigHandler {
	return &ConfigHandler{DataManager: dm}
}

// GET /config/mentors - List all available mentors
func (h *ConfigHandler) GetMentors(c echo.Context) error {
	return c.JSON(http.StatusOK, h.DataManager.GetMentors())
}

// GET /config/investors - List all investors with traits
func (h *ConfigHandler) GetInvestors(c echo.Context) error {
	return c.JSON(http.StatusOK, h.DataManager.GetInvestors())
}

// GET /config/leaders - List all leaders
func (h *ConfigHandler) GetLeaders(c echo.Context) error {
	return c.JSON(http.StatusOK, h.DataManager.GetLeaders())
}

// GET /config/competencies - List all competency definitions
func (h *ConfigHandler) GetCompetencies(c echo.Context) error {
	if h.DataManager.Config == nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Config not loaded"})
	}
	return c.JSON(http.StatusOK, h.DataManager.Config.Competencies)
}

// GET /config/stages - List all simulation stages
func (h *ConfigHandler) GetStages(c echo.Context) error {
	if h.DataManager.Config == nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Config not loaded"})
	}
	return c.JSON(http.StatusOK, h.DataManager.Config.Stages)
}

// GET /config/stage-weights - Get the stage weight matrix
func (h *ConfigHandler) GetStageWeights(c echo.Context) error {
	if h.DataManager.Config == nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Config not loaded"})
	}
	return c.JSON(http.StatusOK, h.DataManager.Config.StageWeights)
}
