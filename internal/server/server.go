package server

import (
	_ "fusemomo-api/cmd/api/docs"

	"github.com/gofiber/fiber/v3"
)

type FiberServer struct {
	*fiber.App
}

func New() *FiberServer {
	server := &FiberServer{
		App: fiber.New(fiber.Config{
			ServerHeader: "fusemomo-api",
			AppName:      "fusemomo-api",
		}),
	}
	return server
}
