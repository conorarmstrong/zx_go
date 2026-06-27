package dac

// SounDrive is a hardware-faithful model of the Next's SounDrive/Covox/SpecDrum
// stereo DAC block — the real audio/soundrive.vhd combined with the
// mode-dependent CPU-port→channel decode lifted from zxnext.vhd (2658-2664).
//
// Four 8-bit channels (default $80 = mid-scale) sum to a 9-bit stereo pair:
// pcm_L = chA + chB, pcm_R = chC + chD (soundrive.vhd). Which CPU port writes
// which channel depends on the enabled DAC mode (the NextReg port-enable bits):
//
//	sd1  : 1F→A 0F→B 4F→C 5F→D        (soundrive mode 1)
//	sd2  : F1→A F3→B F9→C FB→D        (soundrive mode 2)
//	stAD : 3F→A 5F→D                  (stereo A/D)
//	stBC : 0F→B 4F→C                  (stereo B/C)
//	mono : FB→{A,D} (pentagon), B3→{B,C} (GS covox), DF→{A,D} (specdrum)
//
// All writes are gated by the DAC hardware enable (NR$08 bit3 → dac_hw_en).
// This is the faithful reference; wiring it behind the live port decode (which
// today uses a simplified fixed map in Bank) is a follow-up.
type SounDrive struct {
	chA, chB, chC, chD byte

	// mode enables (NextReg internal_port_enable bits, zxnext.vhd:2429-2435).
	SD1, SD2, StereoAD, StereoBC bool
	MonoFB, MonoBC, MonoDF       bool
	// DACEnable mirrors dac_hw_en (NR$08 bit3); when false all writes are ignored.
	DACEnable bool
}

// NewSounDrive returns a SounDrive with the four channels at mid-scale ($80),
// matching soundrive.vhd's reset state.
func NewSounDrive() *SounDrive {
	return &SounDrive{chA: 0x80, chB: 0x80, chC: 0x80, chD: 0x80}
}

// Reset restores the mid-scale reset state (soundrive.vhd reset_i path).
func (s *SounDrive) Reset() { s.chA, s.chB, s.chC, s.chD = 0x80, 0x80, 0x80, 0x80 }

// WritePort applies a CPU I/O write, routing it to the channel(s) selected by
// the enabled mode per zxnext.vhd:2658-2664. No-op unless the DAC is enabled.
func (s *SounDrive) WritePort(port uint16, val byte) {
	if !s.DACEnable {
		return
	}
	lsb := byte(port)
	monoAD := (lsb == 0xFB && s.MonoFB) || (lsb == 0xDF && s.MonoDF) // mono_AD (2658)
	monoBC := lsb == 0xB3 && s.MonoBC                                // mono_BC (2659)

	if monoAD || (lsb == 0x1F && s.SD1) || (lsb == 0xF1 && s.SD2) || (lsb == 0x3F && s.StereoAD) {
		s.chA = val // port_dac_A (2661)
	}
	if monoBC || (lsb == 0x0F && (s.SD1 || s.StereoBC)) || (lsb == 0xF3 && s.SD2) {
		s.chB = val // port_dac_B (2662)
	}
	if monoBC || (lsb == 0x4F && (s.SD1 || s.StereoBC)) || (lsb == 0xF9 && s.SD2) {
		s.chC = val // port_dac_C (2663)
	}
	if monoAD || (lsb == 0x5F && (s.SD1 || s.StereoAD)) || (lsb == 0xFB && s.SD2) {
		s.chD = val // port_dac_D (2664)
	}
}

// WriteNRMono applies a NextReg "mono" audio mirror write (nr_mono_we): it sets
// both channel A and channel D (soundrive.vhd). Independent of the DAC enable.
func (s *SounDrive) WriteNRMono(val byte) { s.chA, s.chD = val, val }

// WriteNRLeft applies a NextReg "left" audio mirror write (nr_left_we → chB).
func (s *SounDrive) WriteNRLeft(val byte) { s.chB = val }

// WriteNRRight applies a NextReg "right" audio mirror write (nr_right_we → chC).
func (s *SounDrive) WriteNRRight(val byte) { s.chC = val }

// PCM_L returns the 9-bit left output (chA + chB), per soundrive.vhd.
func (s *SounDrive) PCM_L() uint16 { return uint16(s.chA) + uint16(s.chB) }

// PCM_R returns the 9-bit right output (chC + chD), per soundrive.vhd.
func (s *SounDrive) PCM_R() uint16 { return uint16(s.chC) + uint16(s.chD) }
