package version

import (
	"strings"
	"testing"
)

func TestVersionIsRC1(t *testing.T) {
	if Version != "v1.0 RC1" {
		t.Errorf("Version = %q, want v1.0 RC1", Version)
	}
	if !strings.HasPrefix(String(), "zx_go ") || !strings.Contains(String(), Version) {
		t.Errorf("String() = %q, want it to embed the product name + version", String())
	}
}
