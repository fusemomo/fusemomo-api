// @title           Fusemomo API
// @version         1.0
// @description     Behavioral Intelligence for AI Agents that act through APIs.
// @contact.name    Fusemomo Support
// @contact.url     https://fusemomo.com
// @contact.email   team@fusemomo.com
//
// @license.name    MIT
// @license.url     https://opensource.org/licenses/MIT
//
// @host            https://api.fusemomo.com
// @BasePath        /
//
// @securityDefinitions.apikey  ApiKeyAuth
// @in                          header
// @name                        Authorization
// @description                 Bearer token: `Authorization: Bearer fm_live_xxxx`
//
// @securityDefinitions.apikey  SessionAuth
// @in                          cookie
// @name                        fusemomo_session
// @description                 HttpOnly session cookie set by POST /auth/session
package main

import (
	"context"
	"fmt"
	"fusemomo-api/internal/config"
	"fusemomo-api/internal/db"
	"fusemomo-api/internal/server"
	"log"
	"os/signal"
	"syscall"
	"time"

	_ "fusemomo-api/cmd/api/docs" // swaggo generated docs

	"github.com/gofiber/contrib/v3/swaggo"
)

func gracefulShutdown(fiberServer *server.FiberServer, done chan bool) {
	// Create context that listens for the interrupt signal from the OS.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Listen for the interrupt signal.
	<-ctx.Done()

	log.Println("shutting down gracefully, press Ctrl+C again to force")
	stop() // Allow Ctrl+C to force shutdown

	// The context is used to inform the server it has 5 seconds to finish
	// the request it is currently handling
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := fiberServer.ShutdownWithContext(ctx); err != nil {
		log.Printf("Server forced to shutdown with error: %v", err)
	}

	log.Println("Server exiting")

	// Notify the main goroutine that the shutdown is complete
	done <- true
}

func main() {
	// Initialize Database Connection
	pool := db.GetPool()
	defer db.Close()

	server := server.New(pool)
	defer func() {
		if err := server.RateLimiter.Close(); err != nil {
			log.Printf("rate limiter shutdown error: %v", err)
		}
	}()
	server.Get("/swagger/*", swaggo.HandlerDefault)
	server.Get("/docs/*", swaggo.New(swaggo.Config{
		URL:               "/swagger/doc.json",
		OAuth2RedirectUrl: "/swagger/oauth2-redirect.html",
	}))

	if err := server.RegisterFiberRoutes(); err != nil {
		log.Fatalf("Failed to register routes: %v", err)
	}

	// Create a done channel to signal when the shutdown is complete
	done := make(chan bool, 1)

	// Run graceful shutdown in a separate goroutine
	go gracefulShutdown(server, done)

	log.Printf("Starting server on port %d...", config.Envs.API_PORT)
	go func() {
		err := server.Listen(fmt.Sprintf(":%d", config.Envs.API_PORT))
		if err != nil {
			panic(fmt.Sprintf("http server error: %s", err))
		}
	}()

	// Wait for the graceful shutdown to complete
	<-done
	log.Println("Graceful shutdown complete.")
}
