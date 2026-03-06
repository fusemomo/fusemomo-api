package middlewares

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"fusemomo-api/internal/config"
	"fusemomo-api/internal/utils"

	"github.com/gofiber/fiber/v3"
	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// SupabaseJWTMiddleware validates JWTs from Supabase.
func SupabaseJWTMiddleware(db *pgxpool.Pool) fiber.Handler {
	publicKey, err := parseSupabaseJWK(config.Envs.SUPABASE_JWT_JWK)
	if err != nil {
		panic(fmt.Sprintf("Failed to parse Supabase JWK at startup: %v", err))
	}

	return func(c fiber.Ctx) error {
		authHeader := c.Get("Authorization")
		if authHeader == "" {
			return utils.Unauthorized("Missing authorization header")
		}

		parts := strings.Split(authHeader, " ")
		if len(parts) != 2 || parts[0] != "Bearer" {
			return utils.Unauthorized("Invalid authorization header format")
		}

		tokenString := parts[1]

		// Parse JWT with ES256 public key.
		token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
			if _, ok := token.Method.(*jwt.SigningMethodECDSA); !ok {
				return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
			}
			if token.Method.Alg() != "ES256" {
				return nil, fmt.Errorf("unexpected algorithm: %v", token.Method.Alg())
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

		// Extract auth.users.ES256id from Supabase JWT (sub claim).
		authUserID, ok := claims["sub"].(string)
		if !ok {
			return utils.Unauthorized("Missing user ID in token")
		}

		// Single DB call: fetch tenant_id, role, and plan together.
		// On first signup, the handle_new_user trigger may not have committed yet
		// (Supabase issues the JWT before the trigger finishes). Retry up to 3 times
		// with a 1-second delay to handle this race condition gracefully.
		var (
			tenantID string
			role     string
			plan     string
		)
		const maxRetries = 3
		const retryDelay = 800 * time.Millisecond
		var tenantErr error
		for attempt := 0; attempt < maxRetries; attempt++ {
			tenantErr = db.QueryRow(c.Context(),
				`SELECT id, role, plan FROM tenants WHERE auth_user_id = $1 AND deleted_at IS NULL`,
				authUserID,
			).Scan(&tenantID, &role, &plan)
			if tenantErr == nil {
				break
			}
			// Only retry on "no rows" — use errors.Is for type-safe pgx v5 check.
			// A genuine DB error (connection refused, etc.) should fail immediately.
			if !errors.Is(tenantErr, pgx.ErrNoRows) {
				break
			}
			if attempt < maxRetries-1 {
				time.Sleep(retryDelay)
			}
		}
		if tenantErr != nil {
			return utils.Unauthorized("Tenant not found")
		}

		c.Locals("tenant_id", tenantID)
		c.Locals("auth_user_id", authUserID)
		c.Locals("role", role)
		c.Locals("plan", plan)

		return c.Next()
	}
}

// parseSupabaseJWK converts Supabase's JWK to an *ecdsa.PublicKey.
func parseSupabaseJWK(jwkJSON string) (*ecdsa.PublicKey, error) {
	var jwk struct {
		X   string `json:"x"`
		Y   string `json:"y"`
		Crv string `json:"crv"`
		Kty string `json:"kty"`
	}

	if err := json.Unmarshal([]byte(jwkJSON), &jwk); err != nil {
		return nil, fmt.Errorf("failed to unmarshal JWK: %w", err)
	}

	if jwk.Kty != "EC" {
		return nil, fmt.Errorf("expected EC key type, got %s", jwk.Kty)
	}

	if jwk.Crv != "P-256" {
		return nil, fmt.Errorf("expected P-256 curve, got %s", jwk.Crv)
	}

	xBytes, err := base64.RawURLEncoding.DecodeString(jwk.X)
	if err != nil {
		return nil, fmt.Errorf("failed to decode X: %w", err)
	}

	yBytes, err := base64.RawURLEncoding.DecodeString(jwk.Y)
	if err != nil {
		return nil, fmt.Errorf("failed to decode Y: %w", err)
	}

	curve := elliptic.P256()
	pubKey := &ecdsa.PublicKey{
		Curve: curve,
		X:     new(big.Int).SetBytes(xBytes),
		Y:     new(big.Int).SetBytes(yBytes),
	}

	return pubKey, nil
}

// APIKeyMiddleware validates API keys for agent requests.
func APIKeyMiddleware(db *pgxpool.Pool) fiber.Handler {
	return func(c fiber.Ctx) error {
		authHeader := c.Get("Authorization")
		if authHeader == "" {
			return utils.Unauthorized("Missing authorization header")
		}

		parts := strings.Split(authHeader, " ")
		if len(parts) != 2 || parts[0] != "Bearer" {
			return utils.Unauthorized("Invalid authorization header format")
		}

		rawKey := parts[1]

		// Hash the incoming key.
		hash := sha256.Sum256([]byte(rawKey))
		keyHash := hex.EncodeToString(hash[:])

		// Single JOIN query: validate the key and fetch tenant role + plan together.
		var apiKey struct {
			ID        string     `db:"id"`
			TenantID  string     `db:"tenant_id"`
			Status    string     `db:"status"`
			ExpiresAt *time.Time `db:"expires_at"`
			Role      string     `db:"role"`
			Plan      string     `db:"plan"`
		}

		err := db.QueryRow(c.Context(),
			`SELECT ak.id, ak.tenant_id, ak.status, ak.expires_at, t.role, t.plan
			   FROM api_keys ak
			   JOIN tenants t ON t.id = ak.tenant_id
			  WHERE ak.key_hash = $1 AND ak.status = 'active'`,
			keyHash,
		).Scan(&apiKey.ID, &apiKey.TenantID, &apiKey.Status, &apiKey.ExpiresAt, &apiKey.Role, &apiKey.Plan)

		if err != nil {
			return utils.Unauthorized("Invalid API key")
		}

		// Check expiration.
		if apiKey.ExpiresAt != nil && time.Now().After(*apiKey.ExpiresAt) {
			// Lazy update status to 'expired' asynchronously
			go func(id string) {
				db.Exec(context.Background(), `UPDATE api_keys SET status = 'expired', updated_at = NOW() WHERE id = $1`, id)
			}(apiKey.ID)
			return utils.Unauthorized("API key expired")
		}

		// Capture values before the goroutine to avoid touching the recycled Fiber context.
		ip := c.IP()
		keyID := apiKey.ID

		// Update last_used_at and last_used_ip asynchronously.
		go func() {
			db.Exec(context.Background(),
				`UPDATE api_keys
				    SET last_used_at = NOW(), last_used_ip = $1
				  WHERE id = $2`,
				ip, keyID,
			)
		}()

		// Propagate all tenant context so downstream middlewares need no DB calls.
		c.Locals("tenant_id", apiKey.TenantID)
		c.Locals("api_key_id", apiKey.ID)
		c.Locals("role", apiKey.Role)
		c.Locals("plan", apiKey.Plan)

		return c.Next()
	}
}
