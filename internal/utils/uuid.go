package utils

import "github.com/google/uuid"

func UUIDWithoutHyphens() string {
	return uuid.New().String()
}
