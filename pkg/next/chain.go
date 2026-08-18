package next

import "github.com/conorarmstrong/zx_go/pkg/next/nextregs"

// chainOnWrite installs an OnWrite handler that runs `after` once whatever was
// already installed for the register has run.
//
// Every Next subsystem that touches a register another subsystem owns needs
// this. Dispatcher.SetOnWrite REPLACES, with no diagnostic, so a bare install
// silently deletes the previous owner's side effect while the register still
// reads back correctly — NR$15 alone carries the sprite enable, the layer
// priority and three sprite flags. With no previous handler the value is
// stored, which is the dispatcher's own default.
func chainOnWrite(d *nextregs.Dispatcher, reg byte, after func(val byte)) {
	prev := d.OnWriteFn(reg)
	d.SetOnWrite(reg, func(disp *nextregs.Dispatcher, val byte) {
		if prev != nil {
			prev(disp, val)
		} else {
			disp.Store(reg, val)
		}
		after(val)
	})
}
