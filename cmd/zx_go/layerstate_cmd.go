package main

import (
	"fmt"
	"strings"
)

// sluPriority decodes NR$15 bits 4-2 into the layer-priority order.
// 110/111 are ULA+Layer2 colour-blend modes (sprites on top).
var sluPriority = [8]string{
	"S>L>U", "L>S>U", "S>U>L", "L>U>S",
	"U>S>L", "U>L>S", "S>(U+L blend)", "(U+S blend)>L",
}

// l2Resolution decodes NR$70 bits 5-4.
var l2Resolution = [4]string{"256x192", "320x256", "640x256", "?"}

// formatLayerState renders the decoded video-layer configuration from
// the relevant NextRegs, read via rd. Pure function (reader injected)
// for unit testing without an emulator.
func formatLayerState(rd func(byte) byte) string {
	var b strings.Builder
	b.WriteString("OK Layers\r\n")

	r15 := rd(0x15)
	fmt.Fprintf(&b, "  $15 layer priority = %s\r\n", sluPriority[(r15>>2)&0x07])
	fmt.Fprintf(&b, "  sprites: %s   over-border: %s   sprite0-on-top: %s\r\n",
		onOff(r15&0x01 != 0), yesNo(r15&0x02 != 0), yesNo(r15&0x40 != 0))

	r68 := rd(0x68)
	fmt.Fprintf(&b, "  ULA   : %s ($68=$%02X)\r\n", enDis(r68&0x80 == 0), r68)

	r70 := rd(0x70)
	fmt.Fprintf(&b, "  Layer2: resolution %s ($70=$%02X)\r\n",
		l2Resolution[(r70>>4)&0x03], r70)

	r6b := rd(0x6B)
	fmt.Fprintf(&b, "  tilemap: %s ($6B=$%02X)\r\n", enDis(r6b&0x80 != 0), r6b)
	return b.String()
}

func onOff(v bool) string {
	if v {
		return "on"
	}
	return "off"
}

func yesNo(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}

func enDis(v bool) string {
	if v {
		return "enabled"
	}
	return "disabled"
}

// cmdLayerState is the `layer-state` debugger command — a decoded
// snapshot of the live video-layer configuration.
func (d *remoteDebugger) cmdLayerState() string {
	if d.emu == nil || d.emu.nextRegs == nil {
		return "ERR layer-state: no NextReg device (not a Next machine?)"
	}
	return formatLayerState(d.emu.nextRegs.Raw)
}
