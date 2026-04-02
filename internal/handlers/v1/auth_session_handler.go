package v1

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"fusemomo-api/internal/config"
	"fusemomo-api/internal/session"
	"fusemomo-api/internal/utils"

	"github.com/gofiber/fiber/v3"
	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5"
)

// SessionStore must be set by the server before routes are registered.
var SessionStore *session.Store

func parseJWK(jwkJSON string) (*ecdsa.PublicKey, error) {
	var jwk struct {
		X   string `json:"x"`
		Y   string `json:"y"`
		Crv string `json:"crv"`
		Kty string `json:"kty"`
	}
	if err := json.Unmarshal([]byte(jwkJSON), &jwk); err != nil {
		return nil, fmt.Errorf("failed to unmarshal JWK: %w", err)
	}
	if jwk.Kty != "EC" || jwk.Crv != "P-256" {
		return nil, fmt.Errorf("expected EC P-256 key, got kty=%s crv=%s", jwk.Kty, jwk.Crv)
	}
	xBytes, err := base64.RawURLEncoding.DecodeString(jwk.X)
	if err != nil {
		return nil, fmt.Errorf("failed to decode X: %w", err)
	}
	yBytes, err := base64.RawURLEncoding.DecodeString(jwk.Y)
	if err != nil {
		return nil, fmt.Errorf("failed to decode Y: %w", err)
	}
	return &ecdsa.PublicKey{
		Curve: elliptic.P256(),
		X:     new(big.Int).SetBytes(xBytes),
		Y:     new(big.Int).SetBytes(yBytes),
	}, nil
}

// CreateSessionHandler godoc
// @Summary      Create a server-side session
// @Description  Validates a Supabase access_token (ES256 JWT), looks up the tenant record, creates
//               a server-side session, and sets an HttpOnly Secure cookie (fusemomo_session).
//               The cookie must be present on all subsequent /app/* and /dashboard/* requests.
// @Tags         Auth
// @Accept       json
// @Produce      json
// @Param        request body object{access_token=string} true "Supabase access token obtained from the Supabase client SDK"
// @Success      200 {object} object{user=object{tenant_id=string,auth_user_id=string,role=string,plan=string}}
// @Failure      400 {object} utils.APIError
// @Failure      401 {object} utils.APIError
// @Failure      500 {object} utils.APIError
// @Router       /auth/session [post]
func (h *Handler) CreateSessionHandler(c fiber.Ctx) error {
	var body struct {
		AccessToken string `json:"access_token"`
	}
	if err := c.Bind().JSON(&body); err != nil || body.AccessToken == "" {
		return utils.BadRequest("access_token is required")
	}

	// Parse Supabase JWK to validate the incoming token.
	publicKey, err := parseJWK(config.Envs.SUPABASE_JWT_JWK)
	if err != nil {
		return utils.InternalServerError("Auth configuration error")
	}

	// Validate the JWT.
	token, err := jwt.Parse(body.AccessToken, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodECDSA); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		if t.Method.Alg() != "ES256" {
			return nil, fmt.Errorf("unexpected algorithm: %v", t.Method.Alg())
		}
		return publicKey, nil
	})
	if err != nil || !token.Valid {
		return utils.Unauthorized("Invalid Supabase token")
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return utils.Unauthorized("Invalid token claims")
	}

	authUserID, ok := claims["sub"].(string)
	if !ok || authUserID == "" {
		return utils.Unauthorized("Missing user ID in token")
	}

	var (
		tenantID  string
		role      string
		plan      string
		tenantErr error
	)
	for attempt := 0; attempt < 3; attempt++ {
		tenantErr = h.DB.QueryRow(c.Context(),
			`SELECT id, role, plan FROM tenants WHERE auth_user_id = $1 AND deleted_at IS NULL`,
			authUserID,
		).Scan(&tenantID, &role, &plan)
		if tenantErr == nil {
			break
		}
		if !errors.Is(tenantErr, pgx.ErrNoRows) {
			break
		}
		if attempt < 2 {
			time.Sleep(800 * time.Millisecond)
		}
	}
	if tenantErr != nil {
		return utils.Unauthorized("Tenant not found")
	}

	// Create server session.
	sess, err := SessionStore.Create(tenantID, authUserID, role, plan)
	if err != nil {
		return utils.InternalServerError("Failed to create session")
	}

	// Set HttpOnly Secure cookie — JS can never read this.
	isProduction := strings.ToLower(config.Envs.ENV) == "production"
	c.Cookie(&fiber.Cookie{
		Name:     session.CookieName,
		Value:    sess.ID,
		HTTPOnly: true,
		Secure:   isProduction,
		SameSite: "Strict",
		Path:     "/",
		MaxAge:   int(session.SessionTTL.Seconds()),
	})

	return c.JSON(fiber.Map{
		"user": fiber.Map{
			"tenant_id":    tenantID,
			"auth_user_id": authUserID,
			"role":         role,
			"plan":         plan,
		},
	})
}

// DeleteSessionHandler godoc
// @Summary      Destroy the current session (logout)
// @Description  Deletes the server-side session record and clears the HttpOnly session cookie.
//               Returns 204 No Content. Safe to call even if no session exists.
// @Tags         Auth
// @Security     SessionAuth
// @Produce      json
// @Success      204 "Session destroyed. Cookie cleared."
// @Router       /auth/session [delete]
func (h *Handler) DeleteSessionHandler(c fiber.Ctx) error {
	sessionID := c.Cookies(session.CookieName)
	if sessionID != "" {
		SessionStore.Delete(sessionID)
	}

	// Clear the cookie.
	c.Cookie(&fiber.Cookie{
		Name:     session.CookieName,
		Value:    "",
		HTTPOnly: true,
		Secure:   true,
		SameSite: "Strict",
		Path:     "/",
		MaxAge:   -1,
	})

	return c.SendStatus(fiber.StatusNoContent)
}
