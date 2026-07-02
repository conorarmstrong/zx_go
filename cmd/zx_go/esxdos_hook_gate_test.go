package main

import "testing"

// When a raw SD-card image is configured, the guest's own divMMC/+3DOS
// code must do all filesystem work against that image — installing the
// host-directory esxDOS RST8 shim alongside it would create a
// split-brain filesystem (host dir and image can disagree on
// contents). The shim exists only as a convenience for host-directory
// (BuildFAT16) mode.
func TestESXDOSHostHookGate(t *testing.T) {
	cases := []struct {
		img, root string
		want      bool
	}{
		{"", "/some/sd/dir", true},           // host-dir mode: shim on
		{"/card.img", "/some/sd/dir", false}, // image mode: guest-only
		{"/card.img", "", false},
		{"", "", false}, // nothing to serve
	}
	for _, c := range cases {
		if got := useESXDOSHostHook(c.img, c.root); got != c.want {
			t.Errorf("useESXDOSHostHook(%q,%q) = %v, want %v", c.img, c.root, got, c.want)
		}
	}
}
