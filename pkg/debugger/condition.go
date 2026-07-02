package debugger

import (
	"fmt"
	"strconv"
	"strings"
)

// Condition is a parsed breakpoint guard expression. Evaluate
// returns true when the breakpoint should fire — the caller (CPU
// PreFetchHook / BreakpointCheck) gates the actual halt on the
// result.
//
// Grammar (small enough to write by hand, big enough for
// compound guards like `pc == $6010 && iff1 == 0`):
//
//	expr  := term  ( "||" term )*
//	term  := cmp   ( "&&" cmp  )*
//	cmp   := atom OP atom        | atom "in" range
//	OP    := "==" | "!=" | "<" | "<=" | ">" | ">="
//	range := atom "-" atom
//	atom  := hex | dec | reg | "read" hex
//	hex   := "$" XX..XX | "0x" XX..XX
//	dec   := 0-9
//	reg   := "pc" | "sp" | "a" | "f" | "b" | "c" | "d" | "e" |
//	         "h" | "l" | "ix" | "iy" | "iff1" | "iff2" | "im" |
//	         "halted" | "bank"
//
// Examples:
//
//	pc == $6010 && iff1 == 0
//	read $5C78 != $00 || a == $FF
//	pc in $1FFA-$3FFF && bank == 0
type Condition struct {
	root node
}

// State carries the minimal CPU + memory view a Condition needs to
// evaluate. The actual emulator wraps its z80.CPU + memory.Memory
// in a thin adapter so the condition package stays free of those
// concrete dependencies — keeps pkg/debugger importable from
// anywhere without dragging the full Z80 + memory weight along.
type State interface {
	Reg(name string) (int64, bool)
	ReadMem(addr uint16) byte
}

// Eval evaluates the parsed condition against the current state.
// Errors are programmer bugs (e.g. unknown register name caught at
// parse time would mean parse let it through); evaluation itself
// is total once the AST is built.
func (c Condition) Eval(s State) bool {
	if c.root == nil {
		return true
	}
	return c.root.eval(s) != 0
}

// String reprints the condition. Useful for `list-breakpoints`
// output so the guard is visible without re-parsing.
func (c Condition) String() string {
	if c.root == nil {
		return ""
	}
	return c.root.String()
}

// node is the AST interface. Internal.
type node interface {
	eval(s State) int64
	String() string
}

// --- parser ---

// ParseCondition tokenizes + parses an expression. Returns an
// error positioning the failure within the input.
func ParseCondition(input string) (Condition, error) {
	p := &parser{tok: tokenize(input)}
	root, err := p.parseExpr()
	if err != nil {
		return Condition{}, err
	}
	if p.pos < len(p.tok) {
		return Condition{}, fmt.Errorf("trailing token %q at position %d",
			p.tok[p.pos].text, p.tok[p.pos].pos)
	}
	return Condition{root: root}, nil
}

type tokenKind int

const (
	tkEOF tokenKind = iota
	tkIdent
	tkHex
	tkDec
	tkOp
)

type token struct {
	kind tokenKind
	text string
	pos  int
}

func tokenize(input string) []token {
	var out []token
	i := 0
	for i < len(input) {
		ch := input[i]
		if ch == ' ' || ch == '\t' {
			i++
			continue
		}
		start := i
		switch {
		case ch == '$':
			i++
			for i < len(input) && isHexDigit(input[i]) {
				i++
			}
			out = append(out, token{tkHex, input[start:i], start})
		case ch == '0' && i+1 < len(input) && (input[i+1] == 'x' || input[i+1] == 'X'):
			i += 2
			for i < len(input) && isHexDigit(input[i]) {
				i++
			}
			out = append(out, token{tkHex, input[start:i], start})
		case ch >= '0' && ch <= '9':
			for i < len(input) && input[i] >= '0' && input[i] <= '9' {
				i++
			}
			out = append(out, token{tkDec, input[start:i], start})
		case (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || ch == '_':
			for i < len(input) && (isAlphaNum(input[i]) || input[i] == '_') {
				i++
			}
			out = append(out, token{tkIdent, strings.ToLower(input[start:i]), start})
		default:
			// Operator: ==, !=, <=, >=, <, >, &&, ||, -
			switch {
			case ch == '=' && i+1 < len(input) && input[i+1] == '=':
				out = append(out, token{tkOp, "==", start})
				i += 2
			case ch == '!' && i+1 < len(input) && input[i+1] == '=':
				out = append(out, token{tkOp, "!=", start})
				i += 2
			case ch == '<' && i+1 < len(input) && input[i+1] == '=':
				out = append(out, token{tkOp, "<=", start})
				i += 2
			case ch == '>' && i+1 < len(input) && input[i+1] == '=':
				out = append(out, token{tkOp, ">=", start})
				i += 2
			case ch == '&' && i+1 < len(input) && input[i+1] == '&':
				out = append(out, token{tkOp, "&&", start})
				i += 2
			case ch == '|' && i+1 < len(input) && input[i+1] == '|':
				out = append(out, token{tkOp, "||", start})
				i += 2
			case ch == '<' || ch == '>' || ch == '-':
				out = append(out, token{tkOp, string(ch), start})
				i++
			default:
				out = append(out, token{tkOp, string(ch), start})
				i++
			}
		}
	}
	return out
}

func isHexDigit(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

func isAlphaNum(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

type parser struct {
	tok []token
	pos int
}

func (p *parser) peek() token {
	if p.pos >= len(p.tok) {
		return token{kind: tkEOF}
	}
	return p.tok[p.pos]
}

func (p *parser) next() token {
	t := p.peek()
	if p.pos < len(p.tok) {
		p.pos++
	}
	return t
}

func (p *parser) parseExpr() (node, error) {
	left, err := p.parseTerm()
	if err != nil {
		return nil, err
	}
	for p.peek().kind == tkOp && p.peek().text == "||" {
		p.next()
		right, err := p.parseTerm()
		if err != nil {
			return nil, err
		}
		left = &binOp{op: "||", left: left, right: right}
	}
	return left, nil
}

func (p *parser) parseTerm() (node, error) {
	left, err := p.parseCmp()
	if err != nil {
		return nil, err
	}
	for p.peek().kind == tkOp && p.peek().text == "&&" {
		p.next()
		right, err := p.parseCmp()
		if err != nil {
			return nil, err
		}
		left = &binOp{op: "&&", left: left, right: right}
	}
	return left, nil
}

func (p *parser) parseCmp() (node, error) {
	left, err := p.parseAtom()
	if err != nil {
		return nil, err
	}
	t := p.peek()
	// `in` range check
	if t.kind == tkIdent && t.text == "in" {
		p.next()
		lo, err := p.parseAtom()
		if err != nil {
			return nil, fmt.Errorf("expected lo bound after `in`: %w", err)
		}
		dash := p.next()
		if dash.kind != tkOp || dash.text != "-" {
			return nil, fmt.Errorf("expected `-` in range, got %q at %d", dash.text, dash.pos)
		}
		hi, err := p.parseAtom()
		if err != nil {
			return nil, fmt.Errorf("expected hi bound: %w", err)
		}
		return &inRange{val: left, lo: lo, hi: hi}, nil
	}
	if t.kind != tkOp {
		return nil, fmt.Errorf("expected comparison operator after atom, got %q at %d",
			t.text, t.pos)
	}
	switch t.text {
	case "==", "!=", "<", "<=", ">", ">=":
	default:
		return nil, fmt.Errorf("not a comparison operator: %q at %d", t.text, t.pos)
	}
	p.next()
	right, err := p.parseAtom()
	if err != nil {
		return nil, err
	}
	return &binOp{op: t.text, left: left, right: right}, nil
}

func (p *parser) parseAtom() (node, error) {
	t := p.next()
	switch t.kind {
	case tkHex:
		s := strings.TrimPrefix(strings.TrimPrefix(t.text, "0x"), "$")
		s = strings.TrimPrefix(s, "0X")
		v, err := strconv.ParseInt(s, 16, 64)
		if err != nil {
			return nil, fmt.Errorf("bad hex literal %q at %d: %w", t.text, t.pos, err)
		}
		return &literal{val: v, src: t.text}, nil
	case tkDec:
		v, err := strconv.ParseInt(t.text, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("bad decimal %q at %d: %w", t.text, t.pos, err)
		}
		return &literal{val: v, src: t.text}, nil
	case tkIdent:
		switch t.text {
		case "read", "peek":
			arg, err := p.parseAtom()
			if err != nil {
				return nil, fmt.Errorf("expected address after `%s`: %w", t.text, err)
			}
			return &readMem{addr: arg}, nil
		default:
			if !isKnownRegister(t.text) {
				return nil, fmt.Errorf("unknown register or keyword %q at %d", t.text, t.pos)
			}
			return &regRef{name: t.text}, nil
		}
	}
	return nil, fmt.Errorf("unexpected token %q at %d", t.text, t.pos)
}

var knownRegisters = map[string]bool{
	"pc": true, "sp": true,
	"a": true, "f": true, "b": true, "c": true, "d": true, "e": true,
	"h": true, "l": true, "ix": true, "iy": true,
	"iff1": true, "iff2": true, "im": true, "halted": true,
	"bank": true,
	"dmmc": true, "automap": true, "mapram": true, "conmem": true,
}

func isKnownRegister(name string) bool { return knownRegisters[name] }

// --- AST nodes ---

type literal struct {
	val int64
	src string
}

func (n *literal) eval(_ State) int64 { return n.val }
func (n *literal) String() string {
	if n.src != "" {
		return n.src
	}
	return strconv.FormatInt(n.val, 10)
}

type regRef struct{ name string }

func (n *regRef) eval(s State) int64 {
	v, _ := s.Reg(n.name)
	return v
}
func (n *regRef) String() string { return n.name }

type readMem struct{ addr node }

func (n *readMem) eval(s State) int64 {
	return int64(s.ReadMem(uint16(n.addr.eval(s) & 0xFFFF)))
}
func (n *readMem) String() string { return "read " + n.addr.String() }

type binOp struct {
	op    string
	left  node
	right node
}

func (n *binOp) eval(s State) int64 {
	l := n.left.eval(s)
	r := n.right.eval(s)
	switch n.op {
	case "==":
		if l == r {
			return 1
		}
	case "!=":
		if l != r {
			return 1
		}
	case "<":
		if l < r {
			return 1
		}
	case "<=":
		if l <= r {
			return 1
		}
	case ">":
		if l > r {
			return 1
		}
	case ">=":
		if l >= r {
			return 1
		}
	case "&&":
		if l != 0 && r != 0 {
			return 1
		}
	case "||":
		if l != 0 || r != 0 {
			return 1
		}
	}
	return 0
}
func (n *binOp) String() string {
	return n.left.String() + " " + n.op + " " + n.right.String()
}

type inRange struct {
	val node
	lo  node
	hi  node
}

func (n *inRange) eval(s State) int64 {
	v := n.val.eval(s)
	if v >= n.lo.eval(s) && v <= n.hi.eval(s) {
		return 1
	}
	return 0
}
func (n *inRange) String() string {
	return n.val.String() + " in " + n.lo.String() + "-" + n.hi.String()
}
