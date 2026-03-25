package server

import (
	"context"

	_ "fusemomo-api/cmd/api/docs"
	"fusemomo-api/internal/middlewares/ratelimit"
	"fusemomo-api/internal/session"
	"fusemomo-api/internal/utils"

	"github.com/gofiber/fiber/v3"
	"github.com/jackc/pgx/v5/pgxpool"
)

type FiberServer struct {
	*fiber.App
	DB           *pgxpool.Pool
	RateLimiter  *ratelimit.RateLimiter
	SessionStore *session.Store
}

func New(db *pgxpool.Pool) *FiberServer {
	sessStore := session.New()

	// Start background GC loop that purges expired sessions every 5 min.
	go sessStore.GCLoop(context.Background())

	server := &FiberServer{
		App: fiber.New(fiber.Config{
			ServerHeader: "fusemomo-api",
			AppName:      "fusemomo-api",
			ErrorHandler: utils.ErrorHandler,
		}),
		DB:           db,
		RateLimiter:  ratelimit.NewRateLimiter(ratelimit.DefaultConfig()),
		SessionStore: sessStore,
	}
	return server
}
