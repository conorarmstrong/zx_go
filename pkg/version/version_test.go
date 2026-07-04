package version

import (
	"strings"
	"testing"
)

func TestVersionString(t *testing.T) {
	if Version != "v1.3.6" {
		t.Errorf("Version = %q, want v1.3.6", Version)
	}
	if !strings.HasPrefix(String(), "zx_go ") || !strings.Contains(String(), Version) {
		t.Errorf("String() = %q, want it to embed the product name + version", String())
	}
}
