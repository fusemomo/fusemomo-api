package utils

import (
	"fmt"

	"github.com/gofiber/fiber/v3"
)

type APIError struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Details interface{} `json:"details,omitempty"`
}

func (e *APIError) Error() string {
	return fmt.Sprintf("Code: %d, Message: %s", e.Code, e.Message)
}

func NewAPIError(code int, message string, details interface{}) *APIError {
	return &APIError{
		Code:    code,
		Message: message,
		Details: details,
	}
}

func BadRequest(message string, details ...interface{}) *APIError {
	var det interface{}
	if len(details) > 0 {
		det = details[0]
	}
	return NewAPIError(fiber.StatusBadRequest, message, det)
}

func Unauthorized(message string, details ...interface{}) *APIError {
	var det interface{}
	if len(details) > 0 {
		det = details[0]
	}
	return NewAPIError(fiber.StatusUnauthorized, message, det)
}

func Forbidden(message string, details ...interface{}) *APIError {
	var det interface{}
	if len(details) > 0 {
		det = details[0]
	}
	return NewAPIError(fiber.StatusForbidden, message, det)
}

func NotFound(message string, details ...interface{}) *APIError {
	var det interface{}
	if len(details) > 0 {
		det = details[0]
	}
	return NewAPIError(fiber.StatusNotFound, message, det)
}

func InternalServerError(message string, details ...interface{}) *APIError {
	var det interface{}
	if len(details) > 0 {
		det = details[0]
	}
	return NewAPIError(fiber.StatusInternalServerError, message, det)
}

func ErrorHandler(c fiber.Ctx, err error) error {
	code := fiber.StatusInternalServerError
	message := "Internal Server Error"
	var details interface{}

	if e, ok := err.(*fiber.Error); ok {
		code = e.Code
		message = e.Message
	}

	if e, ok := err.(*APIError); ok {
		code = e.Code
		message = e.Message
		details = e.Details
	} else if err != nil {
		message = err.Error()
	}

	return c.Status(code).JSON(fiber.Map{
		"error": APIError{
			Code:    code,
			Message: message,
			Details: details,
		},
	})
}
