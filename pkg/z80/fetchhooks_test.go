package z80

import "testing"

func TestAddPreFetchHookFiresInOrder(t *testing.T) {
	cpu, mem := createTestCPU()
	cpu.PC = 0x8000
	mem.Write(0x8000, 0x00) // NOP

	var log []string
	cpu.AddPreFetchHook("first", func(pc uint16) { log = append(log, "first") })
	cpu.AddPreFetchHook("second", func(pc uint16) { log = append(log, "second") })

	cpu.executeInstruction()

	if len(log) != 2 || log[0] != "first" || log[1] != "second" {
		t.Errorf("hooks fired in unexpected order: %v", log)
	}
}

func TestLegacyHookFiresBeforeNamedHooks(t *testing.T) {
	cpu, mem := createTestCPU()
	cpu.PC = 0x8000
	mem.Write(0x8000, 0x00)

	var log []string
	cpu.PreFetchHook = func(pc uint16) { log = append(log, "legacy") }
	cpu.AddPreFetchHook("new", func(pc uint16) { log = append(log, "new") })

	cpu.executeInstruction()

	if len(log) != 2 || log[0] != "legacy" || log[1] != "new" {
		t.Errorf("expected legacy first then named: got %v", log)
	}
}

func TestRemovePreFetchHook(t *testing.T) {
	cpu, mem := createTestCPU()
	cpu.PC = 0x8000
	mem.Write(0x8000, 0x00)

	fires := 0
	cpu.AddPreFetchHook("once", func(pc uint16) { fires++ })
	cpu.RemovePreFetchHook("once")

	cpu.executeInstruction()
	if fires != 0 {
		t.Errorf("removed hook still fired %d times", fires)
	}

	// Removing a non-existent hook is a no-op.
	cpu.RemovePreFetchHook("never-registered")
}

func TestAddPreFetchHookReplacesByName(t *testing.T) {
	cpu, mem := createTestCPU()
	cpu.PC = 0x8000
	mem.Write(0x8000, 0x00)

	var which string
	cpu.AddPreFetchHook("h", func(pc uint16) { which = "first" })
	cpu.AddPreFetchHook("h", func(pc uint16) { which = "second" })

	cpu.executeInstruction()
	if which != "second" {
		t.Errorf("re-add by same name should replace: got %q, want %q", which, "second")
	}
}

func TestAddPreFetchHookNilRemoves(t *testing.T) {
	cpu, mem := createTestCPU()
	cpu.PC = 0x8000
	mem.Write(0x8000, 0x00)

	fires := 0
	cpu.AddPreFetchHook("h", func(pc uint16) { fires++ })
	cpu.AddPreFetchHook("h", nil) // nil-fn variant should deregister

	cpu.executeInstruction()
	if fires != 0 {
		t.Errorf("nil-fn add should deregister hook (fires=%d)", fires)
	}
}

func TestPostFetchHookFires(t *testing.T) {
	cpu, mem := createTestCPU()
	cpu.PC = 0x8000
	mem.Write(0x8000, 0x00)

	var seenPC uint16 = 0xFFFF
	cpu.AddPostFetchHook("post", func(pc uint16) { seenPC = pc })

	cpu.executeInstruction()

	if seenPC != 0x8000 {
		t.Errorf("post-fetch saw PC=%#x, want 0x8000 (pre-fetch value)", seenPC)
	}
}
