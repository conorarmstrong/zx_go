package roms

import "embed"

//go:embed data/*
var embeddedROMs embed.FS
