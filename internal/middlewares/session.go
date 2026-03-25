package middlewares

import (
	"fusemomo-api/internal/session"
	"fusemomo-api/internal/utils"

	"github.com/gofiber/fiber/v3"
)

// SessionCookieMiddleware is a drop-in replacement for SupabaseJWTMiddleware.
// It reads the HttpOnly "fm_session" cookie, looks up the server-side session,
// and injects the same c.Locals that downstream handlers expect:
// "tenant_id", "auth_user_id", "role", "plan"
func SessionCookieMiddleware(store *session.Store) fiber.Handler {
	return func(c fiber.Ctx) error {
		sessionID := c.Cookies(session.CookieName)
		if sessionID == "" {
			return utils.Unauthorized("Missing session cookie")
		}

		sess, ok := store.Get(sessionID)
		if !ok {
			// Expired or unknown — clear the stale cookie
			c.Cookie(&fiber.Cookie{
				Name:     session.CookieName,
				Value:    "",
				MaxAge:   -1,
				HTTPOnly: true,
				Secure:   true,
				SameSite: "Strict",
				Path:     "/",
			})
			return utils.Unauthorized("Session expired or invalid")
		}

		c.Locals("tenant_id", sess.TenantID)
		c.Locals("auth_user_id", sess.AuthUserID)
		c.Locals("role", sess.Role)
		c.Locals("plan", sess.Plan)

		return c.Next()
	}
}
