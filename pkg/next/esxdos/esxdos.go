// Package esxdos implements the esxDOS RST 8 API surface used by
// NextZXOS, dot commands and any program that calls into the SD
// card filesystem on the ZX Spectrum Next.
//
// Calling convention (from the esxDOS API spec): a program executes
// RST 8 (single opcode 0xCF) followed inline by a one-byte API
// number. The divMMC ROM at 0x0008 reads that parameter byte,
// dispatches to a handler, bumps the return address past the
// parameter, and RETs. We sidestep the ROM entirely: a pre-fetch
// hook at PC=0x0008 reads the parameter, runs a Go-side handler,
// and adjusts PC and SP so execution resumes at the instruction
// after the parameter byte.
//
// M_GETHANDLE and M_DRVAPI return safe defaults; every other
// unregistered API number returns CF=1 with A=ESX_EINVAL so a
// probing caller sees a clean error rather than executing the
// wrong code. SetMount registers the file API (F_OPEN etc.) once a
// backing filesystem is available.
package esxdos

import (
	"github.com/conorarmstrong/zx_go/pkg/next/rtc"
	"github.com/conorarmstrong/zx_go/pkg/next/sdcard"
	"github.com/conorarmstrong/zx_go/pkg/z80"
)

// esxDOS API numbers. Names and values follow the published
// esxDOS 0.8.7 / NextZXOS API table, subset to the boot path and
// file-API handlers this package implements.
const (
	M_GETHANDLE byte = 0x80
	M_GETDATE   byte = 0x83
	M_DRVAPI    byte = 0x89
	M_TAPEIN    byte = 0x8B
	M_TAPEOUT   byte = 0x8C
	F_OPEN      byte = 0x9A
	F_CLOSE     byte = 0x9B
	F_SYNC      byte = 0x9C
	F_READ      byte = 0x9D
	F_WRITE     byte = 0x9E
	F_SEEK      byte = 0x9F
	F_FGETPOS   byte = 0xA0
	F_FSTAT     byte = 0xA1
	F_FTRUNCATE byte = 0xA2
	F_OPENDIR   byte = 0xA3
	F_READDIR   byte = 0xA4
)

// esxDOS error codes returned in A with CF=1 on failure.
const (
	ESX_OK      byte = 0x00
	ESX_ENOENT  byte = 0x05
	ESX_EIO     byte = 0x08
	ESX_EBADF   byte = 0x09
	ESX_EWRTYPE byte = 0x0A // wrong handle type (e.g. F_READ on dir)
	ESX_EINVAL  byte = 0x16
)

// APIHandler reads its inputs from CPU registers (and from memory
// for path / buffer arguments), performs the operation, and writes
// outputs back to registers (and memory for read buffers).
// Returning a non-nil error causes the dispatcher to set CF=1 and
// A to the error code — the Error type if the error is one, or
// ESX_EIO for any other error type.
//
// On nil-error return the dispatcher clears CF unconditionally;
// see dispatch's flag contract for the implication.
//
// Register-only handlers (M_GETHANDLE, M_DRVAPI) ignore the Memory
// argument; the file API (F_OPEN, F_READ, F_WRITE etc.) consumes it
// for path strings and read/write buffers.
type APIHandler func(cpu *z80.CPU, mem Memory) error

// Error wraps an esxDOS-numeric error so callers can return it
// from handlers and have the dispatcher set A correctly.
type Error struct {
	Code byte
	Msg  string
}

func (e *Error) Error() string { return e.Msg }

// NewError constructs an esxDOS-coded error.
func NewError(code byte, msg string) *Error { return &Error{Code: code, Msg: msg} }

// Dispatcher routes RST 8 calls to registered API handlers. Wire
// it during ModelNext construction:
//
//	d := esxdos.New()
//	cpu.AddPreFetchHook("esxdos", d.HookFunc(cpu, mem))
//
// HookFunc binds to one CPU+Memory pair via its closure. The
// handler table itself is reusable across pairs (call HookFunc
// twice with different arguments), but the closure returned by
// each call is single-bound.
type Dispatcher struct {
	handlers   map[byte]APIHandler
	mount      sdcard.Mount
	files      map[byte]sdcard.Handle
	nextHandle byte
	rtc        *rtc.RTC

	// trace, if non-nil, is invoked at every RST 8 dispatch with
	// the API byte and the live CPU + Memory references. Used by
	// the debugger to surface esxDOS-API activity without baking a
	// log-package dependency into this file.
	trace func(api byte, cpu *z80.CPU, mem Memory)
}

// SetTrace registers a callback invoked at every RST 8 dispatch.
// Pass nil to disconnect.
func (d *Dispatcher) SetTrace(fn func(api byte, cpu *z80.CPU, mem Memory)) {
	d.trace = fn
}

// New returns a dispatcher with the boot-path minimum handler set
// (M_GETHANDLE, M_DRVAPI) pre-registered. File API handlers
// (F_OPEN, F_READ, etc.) are NOT registered until a mount is
// supplied via SetMount — without a backing filesystem they would
// just return ESX_ENOENT for every call anyway.
func New() *Dispatcher {
	d := &Dispatcher{
		handlers: make(map[byte]APIHandler),
		files:    make(map[byte]sdcard.Handle),
	}
	d.Register(M_GETHANDLE, handleMGetHandle)
	d.Register(M_DRVAPI, handleMDrvAPI)
	return d
}

// SetRTC installs the RTC backing the M_GETDATE handler. When
// set, M_GETDATE returns the current host date packed into the
// guest's DE / HL / C registers per esxDOS conventions. Passing
// nil unregisters the handler.
func (d *Dispatcher) SetRTC(r *rtc.RTC) {
	d.rtc = r
	if r == nil {
		d.Register(M_GETDATE, nil)
		return
	}
	d.Register(M_GETDATE, d.handleMGetDate)
}

// handleMGetDate fills the guest's registers with the current
// host date in MS-DOS-style packed form:
//
//	BC = year*512 + month*32 + day
//	DE = hour*2048 + minute*32 + second/2
//	A = 0 on success (CF cleared by the dispatcher contract)
//
// This is the convention esxDOS uses for the SDCC date helper;
// software that calls M_GETDATE through the standard runtime
// (most NextZXOS programs) sees correct values.
func (d *Dispatcher) handleMGetDate(cpu *z80.CPU, _ Memory) error {
	t := d.rtcTime()
	year := t.year
	if year < 1980 {
		year = 1980
	}
	bc := uint16((year-1980)<<9) | uint16(t.month<<5) | uint16(t.day)
	de := uint16(t.hour<<11) | uint16(t.minute<<5) | uint16(t.second/2)
	cpu.SetBC(bc)
	cpu.SetDE(de)
	cpu.A = 0
	return nil
}

// decodeBCD unpacks a single BCD byte (00..99) into decimal.
func decodeBCD(b byte) int { return int(b>>4)*10 + int(b&0x0F) }

func (d *Dispatcher) rtcTime() rtcSnap {
	if d.rtc == nil {
		return rtcSnap{} // zero time
	}
	return rtcSnap{
		year:   2000 + decodeBCD(d.rtc.Read(rtc.RegYear)),
		month:  decodeBCD(d.rtc.Read(rtc.RegMonth)),
		day:    decodeBCD(d.rtc.Read(rtc.RegDayOfMonth)),
		hour:   decodeBCD(d.rtc.Read(rtc.RegHours)),
		minute: decodeBCD(d.rtc.Read(rtc.RegMinutes)),
		second: decodeBCD(d.rtc.Read(rtc.RegSeconds)),
	}
}

// rtcSnap is a private decimal-form date/time tuple used to
// transfer host time from rtc.RTC (BCD) to handleMGetDate without
// repeatedly calling rtc.Read.
type rtcSnap struct {
	year, month, day, hour, minute, second int
}

// CloseAll closes every open file handle and clears the handle table.
// Call it on teardown/reset so the host OS releases the underlying
// file descriptors — on Windows an unclosed handle blocks deletion of
// the backing file (POSIX unlinks an open file fine; Windows does not).
func (d *Dispatcher) CloseAll() {
	for h, fh := range d.files {
		_ = fh.Close()
		delete(d.files, h)
	}
}

// SetMount installs an SD-card backend and registers the esxDOS
// file-API handlers that need it (F_OPEN through F_READDIR).
// Passing nil unregisters those handlers and closes any open files.
func (d *Dispatcher) SetMount(m sdcard.Mount) {
	d.CloseAll()
	d.mount = m
	if m == nil {
		d.Register(F_OPEN, nil)
		d.Register(F_CLOSE, nil)
		d.Register(F_SYNC, nil)
		d.Register(F_READ, nil)
		d.Register(F_WRITE, nil)
		d.Register(F_SEEK, nil)
		d.Register(F_FGETPOS, nil)
		d.Register(F_FSTAT, nil)
		d.Register(F_FTRUNCATE, nil)
		d.Register(F_OPENDIR, nil)
		d.Register(F_READDIR, nil)
		return
	}
	d.Register(F_OPEN, d.handleFOpen)
	d.Register(F_CLOSE, d.handleFClose)
	d.Register(F_SYNC, d.handleFSync)
	d.Register(F_READ, d.handleFRead)
	d.Register(F_WRITE, d.handleFWrite)
	d.Register(F_SEEK, d.handleFSeek)
	d.Register(F_FGETPOS, d.handleFGetPos)
	d.Register(F_FSTAT, d.handleFStat)
	d.Register(F_FTRUNCATE, d.handleFTruncate)
	d.Register(F_OPENDIR, d.handleFOpenDir)
	d.Register(F_READDIR, d.handleFReadDir)
}

// Register installs (or replaces) a handler for the given API
// number. Pass nil to remove a previously-registered handler.
func (d *Dispatcher) Register(api byte, fn APIHandler) {
	if fn == nil {
		delete(d.handlers, api)
		return
	}
	d.handlers[api] = fn
}

// Memory is the interface the dispatcher needs from the memory
// bus: read for the parameter byte and any input strings, write
// for output buffers (F_READ in particular). Declared locally so
// the package doesn't depend on pkg/memory.
type Memory interface {
	Read(addr uint16) byte
	Write(addr uint16, val byte)
}

// OverlayProbe reports whether the divMMC overlay is currently
// paged in at $0000-$1FFF. The esxDOS hook uses this to decide
// whether RST $08 should be intercepted: on real hardware the
// esxDOS API entry only exists while the divMMC ROM is masking
// the low memory window.
//
// pkg/next/divmmc.Pager satisfies this interface via its
// IsPagedIn method.
type OverlayProbe interface {
	IsPagedIn() bool
}

// HookFunc returns a pre-fetch callback that, when registered via
// cpu.AddPreFetchHook, fires the esxDOS dispatcher exactly when
// PC reaches $0008 AND the divMMC overlay is currently paged in.
// On real Spectrum Next hardware, esxDOS is a ROM that lives in
// the divMMC overlay; its RST $08 handler is only the active
// $0008 vector while the overlay is paged. When the overlay is
// not paged, $0008 holds the currently-mapped NextZXOS bank's
// own RST $08 handler — each bank's handler is different code
// that NextZXOS's own internal RST $08 callers depend on. The
// exact bytes vary between NextZXOS releases, so we don't
// enumerate them here — just observe that they are NOT esxDOS API
// dispatchers.
//
// Without the overlay gate, our hook would clobber every bank's
// native RST $08 handler. Bank-2's mount path in particular issues
// `RST $08; DEFB nn nn nn` expecting its own dispatcher; an
// unconditional intercept would treat the first inline byte as an
// esxDOS function code and advance PC by 1 instead of the 3 the
// bank-2 driver consumes, derailing the rest of bank-2.
//
// Pass overlay=nil to install a "fire on every $0008" hook (used
// by classic-model tests that don't have a divMMC at all).
func (d *Dispatcher) HookFunc(cpu *z80.CPU, mem Memory, overlay OverlayProbe) func(pc uint16) {
	return func(pc uint16) {
		if pc != 0x0008 {
			return
		}
		if overlay != nil && !overlay.IsPagedIn() {
			return
		}
		d.dispatch(cpu, mem)
	}
}

// Dispatch is the unconditional RST $08 trap runner. Equivalent
// to HookFunc's callback but skips the divMMC-signature gate.
// Tests use this to drive the dispatcher without first installing
// the divMMC ROM at low memory.
func (d *Dispatcher) Dispatch(cpu *z80.CPU, mem Memory) {
	d.dispatch(cpu, mem)
}

// dispatch performs the actual RST 8 trap: read inline parameter,
// run the handler, pop the stack, advance PC past the parameter.
//
// Flag contract: on handler-returns-nil the dispatcher forces CF=0;
// on handler-returns-Error the dispatcher forces CF=1 and writes A.
// Handlers therefore CANNOT signal "success-with-CF-set" semantics
// (some esxDOS APIs use CF=1 to mean "end of data" — those would
// need a richer handler return type). The registered handler set
// only needs the simple success/error split.
func (d *Dispatcher) dispatch(cpu *z80.CPU, mem Memory) {
	// Stack top holds the return address pushed by RST 8 — i.e.
	// the address of the inline parameter byte.
	paramAddr := uint16(mem.Read(cpu.SP)) | (uint16(mem.Read(cpu.SP+1)) << 8)
	api := mem.Read(paramAddr)
	if d.trace != nil {
		d.trace(api, cpu, mem)
	}

	handler, ok := d.handlers[api]
	if !ok {
		// Unknown API: error-return path.
		cpu.A = ESX_EINVAL
		cpu.F |= z80.FLAG_C
	} else if err := handler(cpu, mem); err != nil {
		cpu.F |= z80.FLAG_C
		if e, isEsx := err.(*Error); isEsx {
			cpu.A = e.Code
		} else {
			cpu.A = ESX_EIO
		}
	} else {
		cpu.F &^= z80.FLAG_C
	}

	// Simulate RET past the inline parameter byte: SP += 2, then
	// jump to paramAddr + 1.
	cpu.SP += 2
	cpu.PC = paramAddr + 1
}

// handleMGetHandle returns a sentinel "dot command file handle" of
// 0 with no error. This package does not maintain dot-command
// state, so the value is meaningless to NextZXOS — but NextZXOS
// only probes this on startup, so a clean success is enough to
// proceed.
func handleMGetHandle(cpu *z80.CPU, _ Memory) error {
	cpu.A = 0
	return nil
}

// handleMDrvAPI is the drive-specific API entry. NextZXOS probes
// it during startup to discover the current drive's capabilities.
// Returns A=0, BC=0 (no features supported) and no error, which the
// OS treats as "ordinary FAT drive, no extras".
func handleMDrvAPI(cpu *z80.CPU, _ Memory) error {
	cpu.A = 0
	cpu.B = 0
	cpu.C = 0
	return nil
}
