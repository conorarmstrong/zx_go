package main

import (
	"fmt"
	"strings"

	"github.com/conorarmstrong/zx_go/pkg/debugger"
)

// contUntilSpec pairs a parsed condition with its source expression
// so BreakpointCheck can log the human-readable form on a hit. Both
// fields move together behind one atomic.Pointer store/load — never
// updated independently — so a concurrent read from the CPU goroutine
// never observes one without the other.
type contUntilSpec struct {
	cond debugger.Condition
	expr string
}

// cmdContUntil arms a one-shot conditional-continue. The CPU resumes
// (or stays running if it was already running) until the expression
// evaluates true at any M1 boundary, then halts. The condition is
// auto-cleared on fire — no `clear` step needed.
//
// Pass no args to inspect/clear the currently-armed expression.
//
// Examples:
//
//	cont-until SP < $2000
//	cont-until A == $FF && bank == 2
//	cont-until pc == $0008      (cheap "run until next RST 8")
//
// Expression grammar matches `set-breakpoint ... if EXPR` — same
// parser, same register set, same hex syntax.
func (d *remoteDebugger) cmdContUntil(args []string) string {
	if len(args) == 0 {
		specP := d.contUntilCond.Load()
		if specP == nil {
			return "OK (no cont-until armed)"
		}
		return fmt.Sprintf("OK cont-until %s (armed)", specP.expr)
	}
	if strings.EqualFold(args[0], "off") || strings.EqualFold(args[0], "clear") {
		d.contUntilCond.Store(nil)
		return "OK cont-until cleared"
	}
	expr := strings.Join(args, " ")
	cond, err := debugger.ParseCondition(expr)
	if err != nil {
		return "ERR condition: " + err.Error()
	}
	d.contUntilCond.Store(&contUntilSpec{cond: cond, expr: expr})
	// Resume execution (caller probably issued `pause` first via an
	// implicit-pause read command).
	d.paused.Store(false)
	return fmt.Sprintf("OK cont-until %s; running", expr)
}
