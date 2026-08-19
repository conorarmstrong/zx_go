package audio

// Clamp16 folds a wider intermediate back into int16, saturating rather than
// wrapping.
//
// Every stage of the mix needs this, because summing the beeper (±20000), the
// AY (a similar magnitude) and a DAC (±8128) leaves int16 range easily, and a
// wrap-around is an audible full-scale pop exactly at the loudest moment. It
// was written five times over — pkg/ay, pkg/saa1099, pkg/ula, pkg/sam and
// pkg/next/dac each had a copy — so the policy could not be changed or tested
// once.
//
// pkg/ula, pkg/sam and pkg/next/dac use this. pkg/ay and pkg/saa1099
// deliberately keep their own: they model chips and importing this package
// would pull the oto sound driver into every binary that links one. Their
// copies say so and point back here.
func Clamp16(v int32) int16 {
	switch {
	case v > 32767:
		return 32767
	case v < -32768:
		return -32768
	default:
		return int16(v)
	}
}

// SaturatingAdd16 adds a wider contribution to a sample, saturating.
func SaturatingAdd16(s int16, contrib int32) int16 { return Clamp16(int32(s) + contrib) }
