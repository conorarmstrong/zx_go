package esxdos

import (
	"testing"
	"time"

	"github.com/conorarmstrong/zx_go/pkg/next/rtc"
	"github.com/conorarmstrong/zx_go/pkg/z80"
)

// memMap is a tiny in-memory bus for the dispatcher tests so we
// don't need pkg/memory.
type memMap map[uint16]byte

func (m memMap) Read(addr uint16) byte       { return m[addr] }
func (m memMap) Write(addr uint16, val byte) { m[addr] = val }

// stubULA satisfies z80.ULA without doing any real work.
type stubULA struct{}

func (stubULA) ReadPort(_ uint16) (byte, bool) { return 0xFF, false }
func (stubULA) WritePort(_ uint16, _ byte)     {}

func setupRST8(t *testing.T, api byte, retAddr uint16) (*z80.CPU, memMap) {
	t.Helper()
	mem := memMap{}
	cpu := z80.New(memBus{mem}, stubULA{})

	// Place the inline parameter byte at the address that "RST 8"
	// would have pushed (where execution will resume from + 1).
	mem[retAddr] = api

	// Simulate the state immediately after the RST 8 instruction
	// fired: PC at the trap (0x0008), SP holding the return
	// address pointing at the parameter byte.
	cpu.PC = 0x0008
	cpu.SP = 0xFF00
	mem[0xFF00] = byte(retAddr & 0xFF)
	mem[0xFF01] = byte(retAddr >> 8)
	return cpu, mem
}

// memBus adapts memMap to the z80.Memory interface.
type memBus struct{ m memMap }

func (b memBus) Read(addr uint16) byte       { return b.m[addr] }
func (b memBus) Write(addr uint16, val byte) { b.m[addr] = val }
func (b memBus) ContendPort(_ uint16)        {}

func TestDispatcherMGetHandleClearsCFAndReturnsHandle(t *testing.T) {
	cpu, mem := setupRST8(t, M_GETHANDLE, 0x1234)
	cpu.F |= z80.FLAG_C // pre-set so we can prove it clears
	d := New()
	d.Dispatch(cpu, mem)

	if cpu.F&z80.FLAG_C != 0 {
		t.Errorf("M_GETHANDLE: CF should be cleared")
	}
	// handleMGetHandle returns handle 0.
	if cpu.A != 0 {
		t.Errorf("M_GETHANDLE: A = %#x, want 0", cpu.A)
	}
	// PC advanced past the parameter byte; SP popped.
	if cpu.PC != 0x1235 {
		t.Errorf("M_GETHANDLE: PC = %#x, want 0x1235", cpu.PC)
	}
	if cpu.SP != 0xFF02 {
		t.Errorf("M_GETHANDLE: SP = %#x, want 0xFF02", cpu.SP)
	}
}

func TestDispatcherMDrvAPI(t *testing.T) {
	cpu, mem := setupRST8(t, M_DRVAPI, 0x4000)
	d := New()
	d.Dispatch(cpu, mem)

	if cpu.F&z80.FLAG_C != 0 {
		t.Errorf("M_DRVAPI: CF should be cleared on success")
	}
	if cpu.A != 0 || cpu.B != 0 || cpu.C != 0 {
		t.Errorf("M_DRVAPI: A/B/C = %#x/%#x/%#x, want 0/0/0", cpu.A, cpu.B, cpu.C)
	}
}

func TestDispatcherUnknownAPIReturnsEINVAL(t *testing.T) {
	cpu, mem := setupRST8(t, 0xFE /* not registered */, 0x4000)
	d := New()
	d.Dispatch(cpu, mem)

	if cpu.F&z80.FLAG_C == 0 {
		t.Errorf("unknown API: CF should be set")
	}
	if cpu.A != ESX_EINVAL {
		t.Errorf("unknown API: A = %#x, want ESX_EINVAL (%#x)", cpu.A, ESX_EINVAL)
	}
	// Still skips the parameter byte.
	if cpu.PC != 0x4001 {
		t.Errorf("unknown API: PC = %#x, want 0x4001", cpu.PC)
	}
}

func TestDispatcherCustomHandlerError(t *testing.T) {
	cpu, mem := setupRST8(t, 0x42, 0x4000)
	d := New()
	d.Register(0x42, func(c *z80.CPU, _ Memory) error {
		return NewError(ESX_ENOENT, "no such file")
	})
	d.Dispatch(cpu, mem)

	if cpu.F&z80.FLAG_C == 0 {
		t.Errorf("custom error: CF should be set")
	}
	if cpu.A != ESX_ENOENT {
		t.Errorf("custom error: A = %#x, want ESX_ENOENT (%#x)", cpu.A, ESX_ENOENT)
	}
}

func TestHookFuncIgnoresNon0008PCs(t *testing.T) {
	cpu, mem := setupRST8(t, M_GETHANDLE, 0x4000)
	d := New()
	pcBefore, spBefore, fBefore := cpu.PC, cpu.SP, cpu.F
	// Hook with no overlay = always-on gate. Fire at non-0x0008 PC.
	d.HookFunc(cpu, mem, nil)(0x1234)
	if cpu.PC != pcBefore || cpu.SP != spBefore || cpu.F != fBefore {
		t.Errorf("non-0x0008 PC should be ignored; PC/SP/F changed")
	}
}

// stubOverlay implements OverlayProbe with a settable paged-in
// state so tests can verify the gate.
type stubOverlay struct{ pagedIn bool }

func (s *stubOverlay) IsPagedIn() bool { return s.pagedIn }

func TestHookFuncSkipsWhenOverlayNotPaged(t *testing.T) {
	cpu, mem := setupRST8(t, M_GETHANDLE, 0x4000)
	d := New()
	overlay := &stubOverlay{pagedIn: false}
	pcBefore, spBefore := cpu.PC, cpu.SP
	d.HookFunc(cpu, mem, overlay)(0x0008)
	if cpu.PC != pcBefore || cpu.SP != spBefore {
		t.Errorf("hook should be inert when overlay not paged in; PC/SP changed")
	}
}

func TestHookFuncDispatchesWhenOverlayPaged(t *testing.T) {
	cpu, mem := setupRST8(t, M_GETHANDLE, 0x4000)
	d := New()
	overlay := &stubOverlay{pagedIn: true}
	pcBefore := cpu.PC
	d.HookFunc(cpu, mem, overlay)(0x0008)
	if cpu.PC == pcBefore {
		t.Errorf("hook should dispatch when overlay paged in; PC unchanged")
	}
}

func TestMGetDateReturnsHostTimeInDOSFormat(t *testing.T) {
	// Frozen time: 2025-03-14 15:09:26 (Pi Day, Friday).
	r := rtc.New()
	r.SetNow(func() time.Time {
		return time.Date(2025, 3, 14, 15, 9, 26, 0, time.UTC)
	})
	d := New()
	d.SetRTC(r)

	cpu, mem := setupRST8(t, M_GETDATE, 0x4000)
	d.Dispatch(cpu, mem)

	if cpu.F&z80.FLAG_C != 0 {
		t.Fatalf("M_GETDATE: CF set, error in A=%#x", cpu.A)
	}
	if cpu.A != 0 {
		t.Errorf("M_GETDATE: A = %#x, want 0 (success)", cpu.A)
	}

	// BC packed: (year-1980)*512 + month*32 + day
	// = 45*512 + 3*32 + 14 = 23040 + 96 + 14 = 23150 = 0x5A6E
	wantBC := uint16((2025-1980)<<9 | 3<<5 | 14)
	if cpu.BC() != wantBC {
		t.Errorf("M_GETDATE: BC = %#x, want %#x", cpu.BC(), wantBC)
	}
	// DE packed: hour*2048 + minute*32 + second/2
	// = 15*2048 + 9*32 + 13 = 30720 + 288 + 13 = 31021 = 0x792D
	wantDE := uint16(15<<11 | 9<<5 | 26/2)
	if cpu.DE() != wantDE {
		t.Errorf("M_GETDATE: DE = %#x, want %#x", cpu.DE(), wantDE)
	}
}

func TestMGetDateUnregisteredWhenRTCNil(t *testing.T) {
	d := New()
	d.SetRTC(nil) // explicit; should be a no-op since default also nil

	cpu, mem := setupRST8(t, M_GETDATE, 0x4000)
	d.Dispatch(cpu, mem)

	if cpu.F&z80.FLAG_C == 0 {
		t.Errorf("M_GETDATE with no RTC: expected ESX_EINVAL (unknown API), CF should be set")
	}
}

func TestDispatcherRegisterNilRemoves(t *testing.T) {
	d := New()
	d.Register(M_GETHANDLE, nil) // remove the default
	cpu, mem := setupRST8(t, M_GETHANDLE, 0x4000)
	d.Dispatch(cpu, mem)
	if cpu.A != ESX_EINVAL || cpu.F&z80.FLAG_C == 0 {
		t.Errorf("after removing M_GETHANDLE, dispatch should hit unknown-API path")
	}
}

// Covers SetTrace (callback fires on dispatch) + the Error type
// wrapper.

func TestSetTrace_FiresOnDispatch(t *testing.T) {
	cpu, mem := setupRST8(t, M_GETHANDLE, 0x4000)
	d := New()
	var gotAPI byte
	fired := false
	d.SetTrace(func(api byte, _ *z80.CPU, _ Memory) {
		fired = true
		gotAPI = api
	})
	d.HookFunc(cpu, mem, nil)(0x0008)
	if !fired {
		t.Fatal("trace callback did not fire on dispatch")
	}
	if gotAPI != M_GETHANDLE {
		t.Errorf("trace api = $%02X, want M_GETHANDLE $%02X", gotAPI, M_GETHANDLE)
	}
	// Disconnect — a second dispatch must not fire the old callback.
	d.SetTrace(nil)
	fired = false
	cpu2, mem2 := setupRST8(t, M_GETHANDLE, 0x4000)
	d.HookFunc(cpu2, mem2, nil)(0x0008)
	if fired {
		t.Error("trace fired after SetTrace(nil)")
	}
}

func TestEsxError(t *testing.T) {
	e := NewError(0x05, "no such file")
	if e.Code != 0x05 {
		t.Errorf("Code = $%02X, want $05", e.Code)
	}
	if e.Error() != "no such file" {
		t.Errorf("Error() = %q, want \"no such file\"", e.Error())
	}
}
