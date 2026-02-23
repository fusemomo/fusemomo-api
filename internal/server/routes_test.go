package server

import (
	v1 "fusemomo-api/internal/handlers/v1"
	"io"
	"net/http"
	"testing"

	"github.com/gofiber/fiber/v3"
)

func TestHandler(t *testing.T) {
	// Create a Fiber app for testing
	app := fiber.New()
	s := &FiberServer{
		App: fiber.New(),
		DB:  nil, // Dummy for route registration test
	}

	h := v1.NewV1Handler(s.DB)
	// Define a route in the Fiber app
	app.Get("/ping", h.PingPongHandler)
	// Create a test HTTP request
	req, err := http.NewRequest("GET", "/ping", nil)
	if err != nil {
		t.Fatalf("error creating request. Err: %v", err)
	}
	// Perform the request
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("error making request to server. Err: %v", err)
	}
	// Your test assertions...
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status OK; got %v", resp.Status)
	}
	expected := "{\"message\":\"pong\"}"
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("error reading response body. Err: %v", err)
	}
	if expected != string(body) {
		t.Errorf("expected response body to be %v; got %v", expected, string(body))
	}
}
