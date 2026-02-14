package middlewares

import (
	"fusemomo-api/internal/utils"
	"os"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/logger"
	"github.com/gofiber/fiber/v3/middleware/requestid"
)

// LoggingMiddleware returns Fiber's official logger middleware with production-ready configuration
func LoggingMiddleware() fiber.Handler {
	return logger.New(logger.Config{
		// Format defines the logging format with the following tags:

		Format: "[${time}] ${status} - ${method} ${path} - ${latency} - IP: ${ip} - RequestID: ${requestid} ${error}\n",

		// CustomTags allows us to define custom tags for the logger
		// In Fiber v3, requestid is stored in context with an unexported key,
		// so we need to use requestid.FromContext() to retrieve it
		CustomTags: map[string]logger.LogFunc{
			"requestid": func(output logger.Buffer, c fiber.Ctx, data *logger.Data, extraParam string) (int, error) {
				return output.WriteString(requestid.FromContext(c))
			},
		},

		// TimeFormat specifies the format for ${time}
		TimeFormat: "2006-01-02T15:04:05Z07:00",

		// TimeZone can be specified, e.g., "UTC" or "America/New_York"
		TimeZone: "Local",

		// Stream is the writer where logs are written
		Stream: os.Stdout,

		// DisableColors set to true to disable colors in the log output
		DisableColors: false,
	})
}

// RequestIDMiddleware returns Fiber's official request ID middleware
func RequestIDMiddleware() fiber.Handler {
	return requestid.New(requestid.Config{
		// Header is the header key where the request ID will be stored
		Header: "X-Request-ID",

		// Generator defines a function to generate unique IDs
		// If not set, uses UUID by default
		Generator: utils.UUIDWithoutHyphens,
	})
}
