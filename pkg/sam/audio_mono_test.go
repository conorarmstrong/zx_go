package sam

import "testing"

// programTone sets the SAA up to emit a left-weighted tone on channel 0, so the
// left and right channels differ and the mono downmix is a meaningful average.
func programTone(m *Machine) {
	m.SAA.WriteRegister(0x08, 0x80) // channel 0 frequency
	m.SAA.WriteRegister(0x10, 0x04) // channel 0 octave
	m.SAA.WriteRegister(0x14, 0x01) // channel 0 tone enable
	m.SAA.WriteRegister(0x00, 0x0F) // channel 0 amplitude: left only
	m.SAA.WriteRegister(0x1C, 0x01) // sound enable
}

func TestGenerateAudioMonoIsStereoAverage(t *testing.T) {
	a := New(make([]byte, PageSize), make([]byte, PageSize))
	b := New(make([]byte, PageSize), make([]byte, PageSize))
	programTone(a)
	programTone(b)

	stereo := make([]int16, 2*SamplesPerFrame)
	mono := make([]int16, SamplesPerFrame)
	a.GenerateAudio(stereo)   // reference stereo on machine a
	b.GenerateAudioMono(mono) // downmix on identical machine b

	nonZero := false
	for i := 0; i < SamplesPerFrame; i++ {
		want := int16((int32(stereo[2*i]) + int32(stereo[2*i+1])) / 2)
		if mono[i] != want {
			t.Fatalf("mono[%d] = %d, want average %d (L=%d R=%d)", i, mono[i], want, stereo[2*i], stereo[2*i+1])
		}
		if mono[i] != 0 {
			nonZero = true
		}
	}
	if !nonZero {
		t.Error("downmix is all silence — the test tone produced no output")
	}
}

func TestGenerateAudioMonoSilentByDefault(t *testing.T) {
	m := New(make([]byte, PageSize), make([]byte, PageSize))
	mono := make([]int16, SamplesPerFrame)
	m.GenerateAudioMono(mono)
	for i, s := range mono {
		if s != 0 {
			t.Fatalf("idle SAA produced non-zero sample mono[%d] = %d", i, s)
		}
	}
}
