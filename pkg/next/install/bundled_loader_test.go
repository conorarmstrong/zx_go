package install

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// The GPLv3 FPGA loader is bundled (embedded), so LoadROM(FPGABootROM)
// must succeed even when nothing is installed on disk — that's what
// makes the Next boot work out-of-box after only the licensed ROMs are
// downloaded. The licensed ROMs must NOT have such a fallback.
func TestBundledFPGALoaderFallback(t *testing.T) {
	// Redirect the install dir to an empty temp dir (nothing on disk).
	redirectConfig(t)

	// The embedded asset itself is present and the right size.
	emb, err := roms.ReadEmbeddedROM(FPGABootROM)
	if err != nil {
		t.Fatalf("embedded %s missing: %v", FPGABootROM, err)
	}
	if len(emb) != 8192 {
		t.Errorf("embedded loader size = %d, want 8192", len(emb))
	}

	// LoadROM(FPGABootROM) falls back to the embedded copy.
	got, err := LoadROM(FPGABootROM)
	if err != nil {
		t.Fatalf("LoadROM(FPGABootROM) with nothing installed = %v, want bundled fallback", err)
	}
	if len(got) != len(emb) || got[0] != emb[0] {
		t.Error("LoadROM did not return the embedded loader bytes")
	}

	// The licensed ROMs have NO fallback — they must still report
	// not-installed so the download flow triggers.
	if _, err := LoadROM(DistroROM); err == nil {
		t.Error("DistroROM must NOT have a bundled fallback (it's licensed)")
	}
	if _, err := LoadROM(DivMMCROM); err == nil {
		t.Error("DivMMCROM must NOT have a bundled fallback (it's licensed)")
	}
}
