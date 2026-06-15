package main

import "testing"

// the development log: when a raw SD-card IMAGE is configured the guest's
// own divMMC/+3DOS code must do ALL filesystem work against the
// image — installing the host-directory esxDOS RST8 shim alongside
// it creates a split-brain FS (the shim answered "c:/nextzxos/
// enMenus.cfg" from the host dir, which only has enMenus-example.cfg,
// so the menu engine's first source-open failed and the staging gave
// up — the Browser-ENTER reboot of task #255). The shim exists only
// as a convenience for the host-dir (BuildFAT16) mode.
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
