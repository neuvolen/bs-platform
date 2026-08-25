package http

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/bnursik/business_surgery_backend/internal/domain/users"
	"github.com/gin-gonic/gin"
	"golang.org/x/oauth2"
)

// LoginRequest represents payload for login
// swagger:model LoginRequest
type LoginRequest struct {
	Email    string `json:"email" binding:"required,email" example:"user@example.com"`
	Password string `json:"password" binding:"required" example:"secret123"`
}

// swagger:model LoginResponse
type LoginResponse struct {
	AccessToken string `json:"accessToken" example:"eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9..."`
	User        struct {
		ID      string `json:"id" example:"b3e1c9a2-4f3a-4f2d-8b1a-1a2b3c4d5e6f"`
		Email   string `json:"email" example:"user@example.com"`
		Name    string `json:"name" example:"John"`
		Surname string `json:"surname" example:"Doe"`
		Role    string `json:"role" example:"participant"`
	} `json:"user"`
}

type AuthHandler struct {
	svc               users.AuthService
	googleOAuthConfig *oauth2.Config
	frontendURL       string
}

// swagger:model OAuthLoginRequest
type OAuthLoginRequest struct {
	Provider   string `json:"provider" binding:"required" example:"google"`
	ProviderID string `json:"providerId" binding:"required" example:"1234567890"`
	Email      string `json:"email" binding:"omitempty,email" example:"user@example.com"`
	Name       string `json:"name" example:"John"`
	Surname    string `json:"surname" example:"Doe"`
}

// RegisterRequest represents user registration payload
// swagger:model RegisterRequest
type RegisterRequest struct {
	Email    string `json:"email" binding:"required,email" example:"newuser@example.com"`
	Password string `json:"password" binding:"required,min=6" example:"strongpassword"`
	Name     string `json:"name" binding:"required" example:"John"`
	Surname  string `json:"surname" binding:"required" example:"Doe"`
}

// RegisterResponse is returned after successful registration
// swagger:model RegisterResponse
type RegisterResponse struct {
	ID      string `json:"id" example:"b3e1c9a2-4f3a-4f2d-8b1a-1a2b3c4d5e6f"`
	Email   string `json:"email" example:"newuser@example.com"`
	Name    string `json:"name" example:"John"`
	Surname string `json:"surname" example:"Doe"`
	Role    string `json:"role" example:"participant"`
}

//swagger:model AccessResponse
type AccessResponse struct {
	AccessToken string `json:"accessToken" example:"eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9..."`
}

func NewAuthHandler(svc users.AuthService, googleOAuthConfig *oauth2.Config, frontendURL string) *AuthHandler {
	return &AuthHandler{
		svc:               svc,
		googleOAuthConfig: googleOAuthConfig,
		frontendURL:       frontendURL,
	}
}

// generateCSRF creates a random 32-byte base64 token
func generateCSRF() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func (h *AuthHandler) setAuthCookies(c *gin.Context, refreshToken string) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     "refresh_token",
		Value:    refreshToken,
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteNoneMode,
		Expires:  time.Now().Add(14 * 24 * time.Hour),
	})
}

// Register godoc
// @Summary      Register new user
// @Description  Create a new user with email, password, name and surname
// @Tags         auth
// @Accept       json
// @Produce      json
// @Param        request body RegisterRequest true "Register request"
// @Success      201 {object} RegisterResponse
// @Failure      400 {object} map[string]string
// @Failure      500 {object} map[string]string
// @Router       /api/v1/auth/register [post]
func (h *AuthHandler) Register(c *gin.Context) {
	var req RegisterRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	u, err := h.svc.Register(c.Request.Context(), req.Email, req.Password, req.Name, req.Surname)
	if err != nil {
		fmt.Println("Error during registration:", err)
		writeError(c, err)
		return
	}

	c.JSON(http.StatusCreated, gin.H{
		"id":      u.ID.String(),
		"email":   u.Email,
		"name":    u.Name,
		"surname": u.Surname,
		"role":    u.Role,
	})
}

// Login godoc
// @Summary      Log in
// @Description  Authenticate user. Returns short-lived access token in JSON and sets HttpOnly refresh_token cookie.
// @Tags         auth
// @Accept       json
// @Produce      json
// @Param        request body LoginRequest true "Login request"
// @Success      200 {object} LoginResponse
// @Header       200 {string} Set-Cookie "Sets refresh_token=...; HttpOnly; Secure; SameSite=None; Secure; SameSite=None"
// @Failure      400 {object} map[string]string
// @Failure      401 {object} map[string]string
// @Failure      500 {object} map[string]string
// @Router       /api/v1/auth/login [post]
func (h *AuthHandler) Login(c *gin.Context) {
	var req LoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	access, refresh, err := h.svc.Login(c.Request.Context(), req.Email, req.Password)
	if err != nil {
		writeError(c, err)
		return
	}

	h.setAuthCookies(c, string(refresh))

	c.JSON(http.StatusOK, gin.H{
		"accessToken": access,
	})
}

// OAuthLogin godoc
// @Summary      OAuth login
// @Description  Logs in or creates a user via OAuth provider and sets cookies like standard login.
// @Tags         auth
// @Accept       json
// @Produce      json
// @Param        request body OAuthLoginRequest true "OAuth login request"
// @Success      200 {object} LoginResponse
// @Header       200 {string} Set-Cookie "Sets refresh_token=...; HttpOnly; Secure; SameSite=None"
// @Failure      400 {object} map[string]string
// @Failure      401 {object} map[string]string
// @Failure      500 {object} map[string]string
// @Router       /api/v1/auth/oauth [post]
func (h *AuthHandler) OAuthLogin(c *gin.Context) {
	var req OAuthLoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	user, access, refresh, err := h.svc.OAuthLogin(
		c.Request.Context(),
		req.Provider,
		req.ProviderID,
		req.Email,
		req.Name,
		req.Surname,
	)
	if err != nil {
		writeError(c, err)
		return
	}

	h.setAuthCookies(c, string(refresh))

	c.JSON(http.StatusOK, gin.H{
		"accessToken": access,
		"user": gin.H{
			"id":      user.ID.String(),
			"email":   user.Email,
			"name":    user.Name,
			"surname": user.Surname,
			"role":    user.Role,
		},
	})
}

// GoogleLogin godoc
// @Summary      Google OAuth login (redirect)
// @Description  Redirects to Google OAuth2 consent screen. Sets oauth_state cookie for CSRF protection.
// @Tags         auth
// @Produce      json
// @Success      307 {string} string "Redirects to Google"
// @Failure      500 {object} map[string]string
// @Router       /api/v1/auth/oauth/google/login [get]
func (h *AuthHandler) GoogleLogin(c *gin.Context) {
	if h.googleOAuthConfig == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "google_oauth_not_configured"})
		return
	}

	state := generateCSRF()
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     "oauth_state",
		Value:    state,
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteNoneMode,
		Expires:  time.Now().Add(10 * time.Minute),
	})

	authURL := h.googleOAuthConfig.AuthCodeURL(state, oauth2.AccessTypeOffline)
	c.Redirect(http.StatusTemporaryRedirect, authURL)
}

// GoogleCallback godoc
// @Summary      Google OAuth callback
// @Description  Handles Google OAuth2 callback, exchanges code, finalizes login, sets cookies, and redirects to frontend (if configured).
// @Tags         auth
// @Produce      json
// @Success      307 {string} string "Redirects to frontend with cookies set"
// @Success      200 {object} map[string]interface{} "Returns tokens and user info when frontend redirect URL is not configured"
// @Failure      400 {object} map[string]string
// @Failure      500 {object} map[string]string
// @Router       /api/v1/auth/oauth/google/callback [get]
func (h *AuthHandler) GoogleCallback(c *gin.Context) {
	if h.googleOAuthConfig == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "google_oauth_not_configured"})
		return
	}

	state := c.Query("state")
	code := c.Query("code")
	if state == "" || code == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing_state_or_code"})
		return
	}

	cookieState, err := c.Cookie("oauth_state")
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     "oauth_state",
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteNoneMode,
		Expires:  time.Unix(0, 0),
	})
	if err != nil || cookieState != state {
		fmt.Println(err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_state"})
		return
	}

	token, err := h.googleOAuthConfig.Exchange(c.Request.Context(), code)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "code_exchange_failed"})
		return
	}

	client := h.googleOAuthConfig.Client(c.Request.Context(), token)
	resp, err := client.Get("https://www.googleapis.com/oauth2/v3/userinfo")
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "failed_to_fetch_userinfo"})
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		c.JSON(http.StatusBadGateway, gin.H{"error": "failed_to_fetch_userinfo"})
		return
	}

	var ui struct {
		Sub        string `json:"sub"`
		Email      string `json:"email"`
		Name       string `json:"name"`
		GivenName  string `json:"given_name"`
		FamilyName string `json:"family_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&ui); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "failed_to_parse_userinfo"})
		return
	}
	if ui.Sub == "" {
		c.JSON(http.StatusBadGateway, gin.H{"error": "invalid_userinfo"})
		return
	}

	name := ui.GivenName
	if name == "" {
		name = ui.Name
	}

	user, access, refresh, err := h.svc.OAuthLogin(
		c.Request.Context(),
		"google",
		ui.Sub,
		ui.Email,
		name,
		ui.FamilyName,
	)
	if err != nil {
		fmt.Println(err)
		writeError(c, err)
		return
	}

	h.setAuthCookies(c, string(refresh))

	if h.frontendURL != "" {
		c.Redirect(http.StatusTemporaryRedirect, h.frontendURL)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"accessToken": access,
		"user": gin.H{
			"id":      user.ID.String(),
			"email":   user.Email,
			"name":    user.Name,
			"surname": user.Surname,
			"role":    user.Role,
		},
	})
}

// Refresh godoc
// @Summary      Refresh access token
// @Description  Exchanges a valid refresh_token cookie for a new access token. Rotates refresh cookie.
// @Tags         auth
// @Produce      json
// @Success      200 {object} AccessResponse
// @Header       200 {string} Set-Cookie "Rotates refresh_token=...; HttpOnly; Secure; SameSite=None"
// @Failure      401 {object} map[string]string "missing or invalid refresh cookie"
// @Failure      500 {object} map[string]string
// @Router       /api/v1/auth/refresh [post]
func (h *AuthHandler) Refresh(c *gin.Context) {
	rt, err := c.Cookie("refresh_token")
	if err != nil || rt == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "missing_refresh"})
		return
	}

	access, newRefresh, err := h.svc.Refresh(c.Request.Context(), users.RefreshToken(rt))
	if err != nil {
		writeError(c, err)
		return
	}

	h.setAuthCookies(c, string(newRefresh))

	c.JSON(http.StatusOK, gin.H{"accessToken": string(access)})
}

// Ping godoc
// @Summary      Ping (protected)
// @Description  Verifies access token and shows user info from claims.
// @Tags         auth
// @Produce      json
// @Security     BearerAuth
// @Param        Authorization header string true "Bearer {accessToken}"
// @Success      200 {object} map[string]interface{}
// @Failure      401 {object} map[string]string
// @Router       /api/v1/auth/ping [get]
func (h *AuthHandler) Ping(c *gin.Context) {
	userID, _ := c.Get("userID")
	role, _ := c.Get("role")

	c.JSON(http.StatusOK, gin.H{
		"msg":    "pong",
		"userID": userID,
		"role":   role,
	})
}

// Logout godoc
// @Summary      Logout
// @Description  Revokes current refresh session (if tracked) and clears refresh_token cookie.
// @Tags         auth
// @Produce      json
// @Success      204
// @Header       204 {string} Set-Cookie "Clears refresh_token=; Expires=..."
// @Failure      500 {object} map[string]string
// @Router       /api/v1/auth/logout [post]
func (h *AuthHandler) Logout(c *gin.Context) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     "refresh_token",
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteNoneMode,
		Expires:  time.Unix(0, 0),
	})

	c.Status(http.StatusNoContent)
}
