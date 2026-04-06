package roms

import "embed"

//go:embed data/*
var embeddedROMs embed.FS

// ReadEmbeddedROM reads a ROM file from the embedded filesystem.
// Returns the data and nil error on success, or nil and an error if not found.
func ReadEmbeddedROM(filename string) ([]byte, error) {
	return embeddedROMs.ReadFile("data/" + filename)
}
