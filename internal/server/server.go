package server

import (
	_ "fusemomo-api/cmd/api/docs"
	"fusemomo-api/internal/utils"

	"github.com/gofiber/fiber/v3"
	"github.com/jackc/pgx/v5/pgxpool"
)

type FiberServer struct {
	*fiber.App
	DB *pgxpool.Pool
}

func New(db *pgxpool.Pool) *FiberServer {
	server := &FiberServer{
		App: fiber.New(fiber.Config{
			ServerHeader: "fusemomo-api",
			AppName:      "fusemomo-api",
			ErrorHandler: utils.ErrorHandler,
		}),
		DB: db,
	}
	return server
}
