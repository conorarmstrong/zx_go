package sam

import _ "embed"

// embeddedROM is the SAM Coupé system ROM v3.0 (the last official MGT release,
// the standard for emulation). It is bundled because its author, Andrew Wright,
// explicitly placed his SAM Coupé titles — including the ROMs — into free
// redistribution (April 2008). Provenance + the exact grant are recorded in
// LICENSES/samcoupe-rom-NOTICE.md. 32 KB, reset entry F3 C3 B0 00 (DI; JP $00B0).
//
//go:embed data/samcoupe.rom
var embeddedROM []byte

// SystemROM returns the bundled SAM Coupé ROM 3.0 image (32 KB).
func SystemROM() []byte { return embeddedROM }

// NewDefault builds a SAM machine from the bundled ROM 3.0.
func NewDefault() (*Machine, error) { return NewFromROM(embeddedROM) }
