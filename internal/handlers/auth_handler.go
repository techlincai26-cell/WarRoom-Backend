package handlers

import (
	"net/http"
	"war-room-backend/internal/db"
	"war-room-backend/internal/models"
	"war-room-backend/internal/services"

	"github.com/golang-jwt/jwt/v5"
    "github.com/google/uuid"
	"github.com/labstack/echo/v4"
    "gorm.io/gorm"
)

type AuthHandler struct {
    AuthService *services.AuthService
}

func NewAuthHandler(as *services.AuthService) *AuthHandler {
    return &AuthHandler{AuthService: as}
}

type RegisterRequest struct {
    Name     string `json:"name"`
    Email    string `json:"email"`
    Password string `json:"password"`
}

type LoginRequest struct {
    Email    string `json:"email"`
    Password string `json:"password"`
}

func (h *AuthHandler) Register(c echo.Context) error {
    req := new(RegisterRequest)
    if err := c.Bind(req); err != nil {
        return c.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid request"})
    }

    // Check if user exists
    var existingUser models.User
    if err := db.DB.Where("email = ?", req.Email).First(&existingUser).Error; err == nil {
         return c.JSON(http.StatusConflict, map[string]string{"error": "Email already registered"})
    }

    hashedPassword, err := h.AuthService.HashPassword(req.Password)
    if err != nil {
        return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Could not hash password"})
    }

    user := models.User{
        ID:       uuid.New().String(),
        Name:     req.Name,
        Email:    req.Email,
        Password: hashedPassword,
    }

    if err := db.DB.Create(&user).Error; err != nil {
        return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Could not create user"})
    }

    return c.JSON(http.StatusCreated, map[string]string{"message": "User registered successfully"})
}

func (h *AuthHandler) Login(c echo.Context) error {
    req := new(LoginRequest)
    if err := c.Bind(req); err != nil {
        return c.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid request"})
    }

    var user models.User
    if err := db.DB.Where("email = ?", req.Email).First(&user).Error; err != nil {
        if err == gorm.ErrRecordNotFound {
             return c.JSON(http.StatusUnauthorized, map[string]string{"error": "Invalid credentials"})
        }
        return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Database error"})
    }

    if !h.AuthService.CheckPasswordHash(req.Password, user.Password) {
        return c.JSON(http.StatusUnauthorized, map[string]string{"error": "Invalid credentials"})
    }

    token, err := h.AuthService.GenerateToken(&user)
    if err != nil {
        return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Could not generate token"})
    }

    return c.JSON(http.StatusOK, map[string]any{
        "token": token,
        "user": map[string]any{
            "id": user.ID,
            "email": user.Email,
            "name": user.Name,
        },
    })
}

func (h *AuthHandler) Me(c echo.Context) error {
    // User is extracted from JWT middleware in protected routes
    // But this handler might need to be protected.
    // For now, assume it's used in a protected group or check context.
    userToken, ok := c.Get("user").(*jwt.Token) // echo-jwt puts token in context "user"
    if !ok {
         return c.JSON(http.StatusUnauthorized, map[string]string{"error": "Unauthorized"})
    }
    claims := userToken.Claims.(jwt.MapClaims)
    userID := claims["user_id"].(string)

    var user models.User
    if err := db.DB.First(&user, "id = ?", userID).Error; err != nil {
        return c.JSON(http.StatusNotFound, map[string]string{"error": "User not found"})
    }

    return c.JSON(http.StatusOK, map[string]any{
        "id": user.ID,
        "email": user.Email,
        "name": user.Name,
    })
}
