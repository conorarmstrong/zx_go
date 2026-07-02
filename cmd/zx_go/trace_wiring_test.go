package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/memory"
	"github.com/conorarmstrong/zx_go/pkg/roms"
	"github.com/conorarmstrong/zx_go/pkg/trace"
	"github.com/conorarmstrong/zx_go/pkg/z80"
)

// TestInstallTraceHooks_PCEventCarriesLiveFrameCounter verifies the
// `pc` trace channel stamps each event with the emulator's live frame
// counter rather than a counter that never advances.
func TestInstallTraceHooks_PCEventCarriesLiveFrameCounter(t *testing.T) {
	mem, err := memory.New(newTestRomDir(t), roms.Model128K)
	if err != nil {
		t.Fatalf("memory.New: %v", err)
	}
	cpu := z80.New(mem, nil)
	emu := &emulator{cpu: cpu, mem: mem}
	emu.frameCounter = 42 // simulate 42 rendered frames before this fetch

	outPath := filepath.Join(t.TempDir(), "trace.jsonl")
	f := &cliFlags{traceChannels: trace.Channels{PC: true}, traceOutput: outPath}

	_, closeFn := installTraceHooks(emu, f)
	cpu.StepInstruction() // fires the trace-pc pre-fetch hook once
	closeFn()

	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read trace output: %v", err)
	}
	if !strings.Contains(string(data), `"frame":42`) {
		t.Errorf("pc trace event missing live frame counter (want frame:42): %s", data)
	}
}
