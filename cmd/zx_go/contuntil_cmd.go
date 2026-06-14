package main

import (
	"fmt"
	"strings"

	"github.com/conorarmstrong/zx_go/pkg/debugger"
)

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
		condP := d.contUntilCond.Load()
		if condP == nil {
			return "OK (no cont-until armed)"
		}
		return fmt.Sprintf("OK cont-until %s (armed)", d.contUntilExpr)
	}
	if strings.EqualFold(args[0], "off") || strings.EqualFold(args[0], "clear") {
		d.contUntilCond.Store(nil)
		d.contUntilExpr = ""
		return "OK cont-until cleared"
	}
	expr := strings.Join(args, " ")
	cond, err := debugger.ParseCondition(expr)
	if err != nil {
		return "ERR condition: " + err.Error()
	}
	d.contUntilCond.Store(&cond)
	d.contUntilExpr = expr
	// Resume execution (caller probably issued `pause` first via an
	// implicit-pause read command).
	d.paused.Store(false)
	return fmt.Sprintf("OK cont-until %s; running", expr)
}
