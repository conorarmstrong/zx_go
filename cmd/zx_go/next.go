package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/storage"
	"fyne.io/fyne/v2/widget"

	"github.com/conorarmstrong/zx_go/pkg/ay"
	"github.com/conorarmstrong/zx_go/pkg/keyboard"
	"github.com/conorarmstrong/zx_go/pkg/memory"
	"github.com/conorarmstrong/zx_go/pkg/next"
	"github.com/conorarmstrong/zx_go/pkg/next/compositor"
	"github.com/conorarmstrong/zx_go/pkg/next/copper"
	"github.com/conorarmstrong/zx_go/pkg/next/dac"
	"github.com/conorarmstrong/zx_go/pkg/next/divmmc"
	"github.com/conorarmstrong/zx_go/pkg/next/dma"
	"github.com/conorarmstrong/zx_go/pkg/next/esxdos"
	"github.com/conorarmstrong/zx_go/pkg/next/install"
	"github.com/conorarmstrong/zx_go/pkg/next/keymap"
	"github.com/conorarmstrong/zx_go/pkg/next/layer2"
	"github.com/conorarmstrong/zx_go/pkg/next/nextregs"
	"github.com/conorarmstrong/zx_go/pkg/next/palette"
	rtcpkg "github.com/conorarmstrong/zx_go/pkg/next/rtc"
	"github.com/conorarmstrong/zx_go/pkg/next/sdcard"
	"github.com/conorarmstrong/zx_go/pkg/next/sprite"
	"github.com/conorarmstrong/zx_go/pkg/next/tilemap"
	uartpkg "github.com/conorarmstrong/zx_go/pkg/next/uart"
	"github.com/conorarmstrong/zx_go/pkg/peripherals"
	"github.com/conorarmstrong/zx_go/pkg/roms"
	"github.com/conorarmstrong/zx_go/pkg/ula"
	"github.com/conorarmstrong/zx_go/pkg/z80"
)

// dmaPortBus adapts the ULA's port dispatch to the zxnDMA's IOBus contract
// (ReadPort returns a bare byte). Used for DMA IO endpoints.
type dmaPortBus struct{ u *ula.ULA }

func (b dmaPortBus) WritePort(port uint16, val byte) { b.u.WritePort(port, val) }
func (b dmaPortBus) ReadPort(port uint16) byte {
	v, _ := b.u.ReadPort(port)
	return v
}

// nextMenuItem returns the Machine → "ZX Spectrum Next" entry.
// Clicking it triggers an in-place model switch — the emulator
// pauses, wires the Next bus on top of the existing CPU/memory/ULA
// instances, reboots, and resumes. No restart required.
func nextMenuItem(requestModelChange func(roms.SpectrumModel)) *fyne.MenuItem {
	return fyne.NewMenuItem("ZX Spectrum Next", func() {
		requestModelChange(roms.ModelNext)
	})
}

// installNextROM is the File → "Install Next ROMs..." action. It
// asks for a Next ROM file (one of the blobs from the official
// NextZXOS distro plus optionally the FPGA bootrom), copies it
// under the user's config directory, computes its SHA-256, and
// reports back. The destination basename is preserved exactly,
// because pkg/memory.setupNext looks up each ROM by a fixed
// filename — installing the wrong-named file (e.g. someone
// renames enNextZX.rom to next.rom) won't be picked up.
func installNextROM(w fyne.Window) {
	fd := dialog.NewFileOpen(func(reader fyne.URIReadCloser, err error) {
		if err != nil {
			dialog.ShowError(err, w)
			return
		}
		if reader == nil {
			return
		}
		path := reader.URI().Path()
		_ = reader.Close()

		result, err := install.InstallROM(path)
		if err != nil {
			dialog.ShowError(fmt.Errorf("failed to install %s: %w", path, err), w)
			return
		}

		// Recognise the well-known filenames so the user knows
		// what role the file fills. The installer preserves the
		// source basename, so picking enNextZX.rom installs as
		// enNextZX.rom — the lookup happens against this fixed
		// name in pkg/memory.setupNext.
		base := filepath.Base(path)
		var role string
		switch base {
		case install.DistroROM:
			role = "✓ Recognised: NextZXOS firmware (REQUIRED for ModelNext)."
		case install.DivMMCROM:
			role = "✓ Recognised: divMMC / esxDOS overlay (enables SD-card and dot-commands)."
		case install.FPGABootROM:
			role = "✓ Recognised: FPGA boot loader (enables proper cold-boot path)."
		default:
			role = fmt.Sprintf(
				"⚠️ Filename %q is not one the emulator looks up. Expected one of:\n • %s\n • %s\n • %s\nFile was installed anyway in case you know what you're doing.",
				base, install.DistroROM, install.DivMMCROM, install.FPGABootROM)
		}

		msg := fmt.Sprintf(
			"%s\n\nInstalled %d bytes to:\n%s\n\nSHA-256:\n%s\n\n"+
				"Record this digest. A future release will gate "+
				"ZX Spectrum Next boot on a pinned set of known-good "+
				"hashes; until then this UI just copies and reports.",
			role, result.Size, result.DestPath, result.SHA256)
		if result.VersionWarning != "" {
			msg += "\n\n⚠️ VERSION MISMATCH WARNING\n\n" + result.VersionWarning
		}
		dialog.ShowInformation("Next ROM Installed", msg, w)
	}, w)
	fd.SetFilter(storage.NewExtensionFileFilter([]string{".rom", ".ROM"}))
	fd.Show()
}

// offerNextROMDownload asks the user whether to fetch the NextZXOS
// ROMs from the official source. The ROMs are licensed and are NOT
// bundled with the emulator. On confirmation it downloads the
// official distro archive (install.DownloadNextROMs), shows live
// progress, installs enNextZX.rom + enNxtmmc.rom and the SD-card
// tree, and on success invokes onSuccess (which retries the model
// switch). Errors are surfaced; the running emulator is untouched
// until the switch is retried.
func offerNextROMDownload(w fyne.Window, onSuccess func()) {
	msg := "The Spectrum Next needs the NextZXOS ROMs, which are\n" +
		"licensed and NOT bundled with this emulator.\n\n" +
		"Download them now from the official Spectrum Next\n" +
		"distribution?\n\n" +
		install.DistroURL() + "\n\n" +
		"This fetches the official archive and installs the ROMs\n" +
		"plus the SD-card contents. (You can also install them\n" +
		"manually via File → Install Next ROMs…)"
	dialog.NewConfirm("Download Spectrum Next ROMs?", msg, func(ok bool) {
		if !ok {
			return
		}
		runNextROMDownload(w, onSuccess)
	}, w).Show()
}

// runNextROMDownload performs the download on a background goroutine
// with a cancellable progress dialog, then installs + reports.
func runNextROMDownload(w fyne.Window, onSuccess func()) {
	prog := widget.NewProgressBar()
	status := widget.NewLabel("Connecting…")
	content := container.NewVBox(status, prog)
	ctx, cancel := context.WithCancel(context.Background())
	dlg := dialog.NewCustom("Downloading Spectrum Next ROMs", "Cancel",
		content, w)
	dlg.SetOnClosed(cancel)
	dlg.Show()

	go func() {
		res, err := install.DownloadNextROMs(ctx, nil, install.DistroURL(),
			func(done, total int64) {
				fyne.Do(func() {
					if total > 0 {
						prog.SetValue(float64(done) / float64(total))
						status.SetText(fmt.Sprintf("Downloaded %.1f / %.1f MB",
							float64(done)/1e6, float64(total)/1e6))
					} else {
						status.SetText(fmt.Sprintf("Downloaded %.1f MB…",
							float64(done)/1e6))
					}
				})
			})
		fyne.Do(func() {
			dlg.Hide()
			cancel()
			if err != nil {
				dialog.ShowError(fmt.Errorf("download failed: %w", err), w)
				return
			}
			dialog.ShowInformation("Spectrum Next ROMs installed",
				fmt.Sprintf("Installed: %s\nSD-card files: %d\n\nStarting the Spectrum Next…",
					strings.Join(res.InstalledROMs, ", "), res.SDFiles), w)
			if onSuccess != nil {
				onSuccess()
			}
		})
	}()
}

// newNextEmulator constructs the production Spectrum Next emulator
// from scratch (cold start). For runtime model switches use the
// classic newEmulator(model) path and let switchModel call
// wireNextSubsystems on the existing struct.
func newNextEmulator() (*emulator, error) {
	kbd := keyboard.New()
	if path := userKeymapPath(); path != "" {
		if err := kbd.LoadOverrides(path); err != nil {
			slog.Warn("failed to load custom keymap", "err", err)
		}
	}
	mem, err := memory.New("roms", roms.ModelNext)
	if err != nil {
		return nil, fmt.Errorf("next: memory.New: %w", err)
	}
	u := ula.New(mem, kbd)
	cpu := z80.New(mem, u)
	// ZX_GO_FORCE_IFF2_3F2C: DIAGNOSTIC ONLY — force IFF1/IFF2 true at the
	// esxDOS dispatch's LD A,I ($3F2C) post-launch, so the esxDOS-exit
	// restores interrupts (the EI the reference emulator takes). Tests whether the launcher
	// hang is caused by interrupts being disabled at the handoff.
	if os.Getenv("ZX_GO_FORCE_IFF2_3F2C") != "" {
		cpu.AddPreFetchHook("force-iff2", func(pc uint16) {
			if pc == 0x3F2C && cpu.InstructionCount() > 23_000_000 {
				cpu.IFF1, cpu.IFF2 = true, true
			}
		})
	}
	// ZX_GO_FORCE_IFF_LAUNCH: DIAGNOSTIC — force interrupts enabled
	// throughout the launch window (except inside the IM1/divMMC ISR
	// range, to avoid nesting), to prove the hang is "launcher runs DI'd".
	if os.Getenv("ZX_GO_FORCE_IFF_LAUNCH") != "" {
		cpu.AddPreFetchHook("force-iff-launch", func(pc uint16) {
			if cpu.InstructionCount() > 22_000_000 {
				cpu.IFF1, cpu.IFF2 = true, true
			}
		})
	}
	// Spec-faithful maskable frame-INT timing (timing.md §1c) is the DEFAULT
	// for the Next. The FPGA asserts int_ula as a NARROW pulse at a fixed
	// point in the ULA frame (zxnext.vhd:2014-2033), not "held the whole
	// frame": an EI that misses the pulse window does NOT take a stale INT.
	// NextZXOS boots in +3/128K timing (NR$03 default "011"). Validated
	// against the reference emulator via --next-memdiff: with the narrow pulse
	// (+ the SpeedMultiplier-scaled frame boundary) our 64 KB logical memory
	// is byte-identical to the reference emulator at $3F1B hit#1, where the legacy
	// held-assert model over-fired the frame INT at 28 MHz. Opt out with
	// ZX_GO_NO_INT_TIMING=1 for held-assert A/B comparison; ZX_GO_INT_ASSERT
	// overrides the assert tstate for frame-origin sweeps.
	//
	// Same setup the Machine-menu switch path uses (configureClassicIntTiming
	// for ModelNext) so direct-boot and switch-to-Next behave identically; the
	// ZX_GO_INT_ASSERT debug override is then layered on top.
	configureClassicIntTiming(cpu, roms.ModelNext)
	if os.Getenv("ZX_GO_NO_INT_TIMING") == "" {
		if v := os.Getenv("ZX_GO_INT_ASSERT"); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				cpu.IntAssertTstate = uint64(n)
			}
		}
	}
	if path := os.Getenv("ZX_GO_SP_LOG"); path != "" {
		if f, err := os.Create(path); err == nil {
			cpu.OnSPLoad = func(pc, oldSP, newSP uint16) {
				_, _ = fmt.Fprintf(f, "PC=$%04X oldSP=$%04X newSP=$%04X\n", pc, oldSP, newSP)
			}
		}
	}
	pm := peripherals.NewPeripheralManager(mem, "roms")
	u.SetPeripherals(pm)
	if cliFlagsActive == nil || !cliFlagsActive.noSound {
		u.EnableAudio()
		configureAudioExtras(u)
	} else {
		slog.Info("--no-sound: audio disabled")
	}

	e := &emulator{
		cpu:          cpu,
		mem:          mem,
		ula:          u,
		kbd:          kbd,
		peripherals:  pm,
		model:        roms.ModelNext,
		physicalKeys: make(map[fyne.KeyName]bool),
		keyQueue:     make(chan keyState, 10),
		stopChan:     make(chan struct{}),
	}
	if err := wireNextSubsystems(e); err != nil {
		return nil, err
	}
	e.paused.Store(true)
	return e, nil
}

// wireNextSubsystems attaches the Spectrum Next bus onto an
// emulator constructed with classic (CPU + mem + ULA + kbd +
// peripherals) wiring. Used by both the cold-start newNextEmulator
// path and the runtime model-switch in switchModel.
//
// What this changes on the existing instances:
//
// - cpu.Variant flips to VariantZ80N (enables Z80N opcodes)
// - cpu.NextRegs is set to a freshly-constructed NextReg
// dispatcher; cpu.AddPreFetchHook installs the divMMC auto-pager
// and the esxDOS RST 8 trap
// - the ULA's SetNext* setters point at NextReg / Layer 2 /
// compositor / sprites / DAC / divMMC instances
// - mem.PeripheralRead and PeripheralWrite are wrapped so the
// divMMC overlay shadows the bottom 16 KB
// - the NMI callback chain is widened so NextZXOS's vector at
// 0x0066 can co-exist with Multiface
// - the audio mixer picks up the DAC bank (SetNextDAC pushes
// through to the running audio system when it's already on)
//
// Pure additive — every change is reversed by unwireNextSubsystems.
// The caller is expected to have paused the emulation goroutine
// before calling this; switchModel takes care of that.
func wireNextSubsystems(e *emulator) error {
	cpu, mem, u, kbd, pm := e.cpu, e.mem, e.ula, e.kbd, e.peripherals

	cpu.Variant = z80.VariantZ80N

	disp := nextregs.New()
	if w := os.Getenv("ZX_GO_NEXTREG_WATCH"); w != "" {
		// Comma-separated list of hex register numbers to log every
		// write to (reg + value + CPU PC). For pinning when guest
		// code finally pokes a specific NextReg — e.g. NR$03 to
		// commit the machine-type transition out of config mode.
		watched := make(map[byte]bool)
		watchAll := strings.TrimSpace(w) == "*"
		for _, part := range strings.Split(w, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			n, err := strconv.ParseUint(
				strings.TrimPrefix(strings.TrimPrefix(part, "0x"), "$"), 16, 8)
			if err == nil {
				watched[byte(n)] = true
			}
		}
		disp.SetTracer(func(reg, val byte, isWrite bool) {
			if !isWrite || (!watchAll && !watched[reg]) {
				return
			}
			slog.Info("nextreg-watch",
				"reg", fmt.Sprintf("$%02X", reg),
				"val", fmt.Sprintf("$%02X", val),
				"pc", fmt.Sprintf("$%04X", cpu.PC))
		})
	}
	if os.Getenv("ZX_GO_NEXTREG_READ_TRACE") != "" {
		readCounts := make(map[byte]uint64)
		writeCounts := make(map[byte]uint64)
		disp.SetTracer(func(reg, val byte, isWrite bool) {
			if isWrite {
				writeCounts[reg]++
			} else {
				readCounts[reg]++
			}
		})
		// Print summary on emulator shutdown via finalizer-like hook.
		// Cheap and well-bounded.
		go func() {
			tick := time.NewTicker(2 * time.Second)
			defer tick.Stop()
			lastReads := make(map[byte]uint64)
			lastWrites := make(map[byte]uint64)
			for range tick.C {
				type kv struct {
					reg   byte
					reads uint64
				}
				var deltas []kv
				for reg, c := range readCounts {
					d := c - lastReads[reg]
					if d > 0 {
						deltas = append(deltas, kv{reg, d})
					}
					lastReads[reg] = c
				}
				sort.Slice(deltas, func(i, j int) bool { return deltas[i].reads > deltas[j].reads })
				for i, kv := range deltas {
					if i >= 10 {
						break
					}
					slog.Info("nextreg-read-trace",
						"reg", fmt.Sprintf("$%02X", kv.reg), "reads_per_2s", kv.reads)
				}
				var wdeltas []kv
				for reg, c := range writeCounts {
					d := c - lastWrites[reg]
					if d > 0 {
						wdeltas = append(wdeltas, kv{reg, d})
					}
					lastWrites[reg] = c
				}
				sort.Slice(wdeltas, func(i, j int) bool { return wdeltas[i].reads > wdeltas[j].reads })
				for i, kv := range wdeltas {
					if i >= 5 {
						break
					}
					slog.Info("nextreg-write-trace",
						"reg", fmt.Sprintf("$%02X", kv.reg), "writes_per_2s", kv.reads)
				}
			}
		}()
	}
	if os.Getenv("ZX_GO_PORT_READ_TRACE") != "" {
		readCounts := make(map[uint16]uint64)
		writeCounts := make(map[uint16]uint64)
		// Optional deep dive: when $ZX_GO_PORT_E3_DEEP_TRACE is set,
		// sample writes to port $E3 (any high byte) with PC + val.
		// Sampling at 1:8192 keeps log volume manageable at the
		// observed 1.85M-writes-per-second rate.
		deepE3 := os.Getenv("ZX_GO_PORT_E3_DEEP_TRACE") != ""
		var e3Sample uint64
		// Per-(PC, val) bucketing for $01E3 writes so we can see
		// the loop's hottest origin sites and what values it's
		// pushing. Indexed by (PC<<8)|val to keep map keys small.
		e3PCValHits := make(map[uint32]uint64)
		u.SetPortTracer(func(addr uint16, val byte, isWrite, handled bool) {
			if isWrite {
				writeCounts[addr]++
				if deepE3 && (addr&0xFF) == 0xE3 {
					e3Sample++
					if e3Sample&0x1FFF == 0 {
						slog.Info("port-E3-sample",
							"port", fmt.Sprintf("$%04X", addr),
							"val", fmt.Sprintf("$%02X", val),
							"pc", fmt.Sprintf("$%04X", cpu.PC),
							"sample", e3Sample)
					}
					if addr == 0x01E3 {
						key := (uint32(cpu.PC) << 8) | uint32(val)
						e3PCValHits[key]++
					}
				}
			} else {
				readCounts[addr]++
			}
		})
		// Capture register state at every entry to divMMC ROM
		// $0059 — the stack-pivot routine. Sample 1:8192 to keep
		// volume manageable.
		if deepE3 {
			var n0059 uint64
			cpu.AddPreFetchHook("e3-0059-trace", func(pc uint16) {
				if pc == 0x0059 {
					n0059++
					if n0059&0x1FFF == 0 {
						sp := cpu.SP
						saved := uint16(mem.Read(0x25B8)) | (uint16(mem.Read(0x25B9)) << 8)
						// Read 16 words from the pivoted-to stack to
						// see the full task-chain pattern. Each
						// $00EE-$00F1 iteration through the divMMC ROM
						// tail pops 6 bytes (POP HL, POP AF, RET) so
						// we want to see at least 5 iterations worth.
						pivots := make([]string, 16)
						for i := 0; i < 16; i++ {
							v := uint16(mem.Read(saved+uint16(i*2))) | (uint16(mem.Read(saved+uint16(i*2+1))) << 8)
							pivots[i] = fmt.Sprintf("$%04X", v)
						}
						slog.Info("e3-0059-entry",
							"sample", n0059,
							"sp", fmt.Sprintf("$%04X", sp),
							"af", fmt.Sprintf("$%04X", uint16(cpu.A)<<8|uint16(cpu.F)),
							"hl", fmt.Sprintf("$%04X", cpu.HL()),
							"25B8", fmt.Sprintf("$%04X", saved),
							"pivot_chain",
							pivots[0]+" "+pivots[1]+" "+pivots[2]+" "+
								pivots[3]+" "+pivots[4]+" "+pivots[5]+" "+
								pivots[6]+" "+pivots[7]+" "+pivots[8]+" "+
								pivots[9]+" "+pivots[10]+" "+pivots[11]+" "+
								pivots[12]+" "+pivots[13]+" "+pivots[14]+" "+
								pivots[15])
					}
				}
			})
		}
		// PC histogram — count PC of every M1 fetch, dump top
		// buckets every 4s. Reveals where the CPU is actually
		// spending its time independent of which port writes
		// dominate.
		if deepE3 {
			var pcHist [0x10000]uint64
			cpu.AddPreFetchHook("e3-pc-hist", func(pc uint16) {
				pcHist[pc]++
			})
			go func() {
				tick := time.NewTicker(4 * time.Second)
				defer tick.Stop()
				var lastHist [0x10000]uint64
				type kv struct {
					pc uint16
					n  uint64
				}
				for range tick.C {
					var deltas []kv
					for pc := 0; pc < 0x10000; pc++ {
						d := pcHist[pc] - lastHist[pc]
						if d > 0 {
							deltas = append(deltas, kv{uint16(pc), d})
						}
						lastHist[pc] = pcHist[pc]
					}
					sort.Slice(deltas, func(i, j int) bool { return deltas[i].n > deltas[j].n })
					for i, kv := range deltas {
						if i >= 15 {
							break
						}
						slog.Info("pc-hist",
							"pc", fmt.Sprintf("$%04X", kv.pc),
							"fetches_per_4s", kv.n)
					}
				}
			}()
		}
		// Periodically dump the top (PC, val) buckets for $01E3.
		if deepE3 {
			go func() {
				tick := time.NewTicker(4 * time.Second)
				defer tick.Stop()
				type kv struct {
					pc  uint16
					val byte
					n   uint64
				}
				last := make(map[uint32]uint64)
				for range tick.C {
					var deltas []kv
					for k, c := range e3PCValHits {
						d := c - last[k]
						if d > 0 {
							deltas = append(deltas, kv{
								pc:  uint16(k >> 8),
								val: byte(k & 0xFF),
								n:   d,
							})
						}
						last[k] = c
					}
					sort.Slice(deltas, func(i, j int) bool { return deltas[i].n > deltas[j].n })
					for i, kv := range deltas {
						if i >= 12 {
							break
						}
						slog.Info("port-01E3-bucket",
							"pc", fmt.Sprintf("$%04X", kv.pc),
							"val", fmt.Sprintf("$%02X", kv.val),
							"writes_per_4s", kv.n)
					}
				}
			}()
		}
		// ZX_GO_CAPTURE_SPIN: dump the last 64 M1 PCs leading into the FIRST
		// $0000 spin after the 128K-BASIC launch (insn-gated past boot). Shows
		// how the NextZXOS launcher derails into bank-2's spin-stub at $0000
		// instead of completing the editor handoff (NR$8C reads-mode).
		if os.Getenv("ZX_GO_CAPTURE_SPIN") != "" {
			ring := make([]uint16, 64)
			var ri int
			var dumped bool
			cpu.AddPreFetchHook("capture-spin", func(pc uint16) {
				ring[ri&63] = pc
				ri++
				if !dumped && pc == 0x0000 && cpu.InstructionCount() > 23_200_000 {
					dumped = true
					var b strings.Builder
					for k := 0; k < 64; k++ {
						fmt.Fprintf(&b, "%04X ", ring[(ri+k)&63])
					}
					slog.Info("spin-entry", "insn", cpu.InstructionCount(),
						"nr8c", fmt.Sprintf("$%02X", mem.AltROMReg()), "rombank", mem.GetROMBank(),
						"last64pc", b.String())
				}
			})
		}
		// ZX_GO_CAPTURE_3F44: at the esxDOS-exit BIT 2,L (ROM2 $3F44) that
		// gates the interrupt-restore EI, dump HL + IFF1/IFF2 + F. the reference emulator
		// takes the EI (L bit2 set, IFF2 was 1); ours skips it. Pins whether
		// our IFF2 / L diverges.
		if os.Getenv("ZX_GO_CAPTURE_3F44") != "" {
			var nE, n090, n88 int
			lastIFF := -1
			prevPC := uint16(0)
			lastDownPrev := uint16(0) // PC that executed the 1->0 transition
			lastDownCur := uint16(0)  // PC reached with IFF1 newly 0
			var lastDownInsn uint64
			var dumped bool
			ring088 := make([]uint16, 280)
			var ri088 int
			var dumped088 bool
			cpu.AddPreFetchHook("capture-3f44", func(pc uint16) {
				if cpu.InstructionCount() < 20_000_000 {
					return
				}
				iff := 0
				if cpu.IFF1 {
					iff = 1
				}
				if iff != lastIFF {
					if lastIFF == 1 && iff == 0 {
						lastDownPrev = prevPC
						lastDownCur = pc
						lastDownInsn = cpu.InstructionCount()
					}
					lastIFF = iff
				}
				prevPC = pc
				if pc == 0x0E37 && nE < 1 {
					nE++
					slog.Info("iff-down-before-0E37",
						"transition_at_pc", fmt.Sprintf("$%04X", lastDownPrev),
						"landed_pc", fmt.Sprintf("$%04X", lastDownCur),
						"at_insn", lastDownInsn,
						"0E37_insn", cpu.InstructionCount(),
						"iff1_at_0E37", cpu.IFF1)
				}
				// Ring-buffer the path INTO the launch-abort $088D (DI+screen
				// clear). the reference emulator doesn't take this path; the branch that leads
				// here is the real divergence.
				ring088[ri088%len(ring088)] = pc
				ri088++
				if !dumped088 && pc == 0x088D && cpu.InstructionCount() > 21_500_000 {
					dumped088 = true
					var b strings.Builder
					for k := 0; k < len(ring088); k++ {
						fmt.Fprintf(&b, "%04X ", ring088[(ri088+k)%len(ring088)])
					}
					slog.Info("path-into-088D", "insn", cpu.InstructionCount(), "last64", b.String())
				}
				if pc == 0x0913 && cpu.InstructionCount() > 21_900_000 && n88 < 3 {
					n88++
					var by strings.Builder
					for a := uint16(0x0913); a <= 0x0925; a++ {
						fmt.Fprintf(&by, "%02X ", mem.Read(a))
					}
					slog.Info("cap-0913bytes", "n", n88, "insn", cpu.InstructionCount(),
						"rombank", mem.GetROMBank(), "8c", fmt.Sprintf("$%02X", mem.AltROMReg()),
						"mmu0_1", fmt.Sprintf("%d %d", mem.GetMMU(0), mem.GetMMU(1)),
						"bytes_913", by.String())
				}
				if pc == 0x08B5 && cpu.InstructionCount() > 21_900_000 && n88 < 6 {
					n88++
					a := cpu.A
					br := "fall->08B7"
					if a&0x40 == 0 {
						br = "JRZ->0913"
					}
					var sd strings.Builder
					for x := uint16(0x5D00); x <= 0x5D45; x++ {
						fmt.Fprintf(&sd, "%02X", mem.Read(x))
					}
					slog.Info("cap-08B5-fork", "n", n88, "insn", cpu.InstructionCount(),
						"mem_5C92", fmt.Sprintf("$%02X", mem.Read(0x5C92)),
						"a", fmt.Sprintf("$%02X", a), "bit6", (a>>6)&1, "branch", br,
						"sysvars_5D00_5D45", sd.String())
				}
				if pc == 0x088D && cpu.InstructionCount() > 21_900_000 && n88 < 99 {
					n88++
					n090++
					var by strings.Builder
					for a := uint16(0x088D); a <= 0x08AC; a++ {
						fmt.Fprintf(&by, "%02X ", mem.Read(a))
					}
					slog.Info("cap-088Dbytes", "n", n090, "insn", cpu.InstructionCount(),
						"rombank", mem.GetROMBank(),
						"mmu", fmt.Sprintf("%d %d %d %d %d %d %d %d", mem.GetMMU(0), mem.GetMMU(1), mem.GetMMU(2), mem.GetMMU(3), mem.GetMMU(4), mem.GetMMU(5), mem.GetMMU(6), mem.GetMMU(7)),
						"8c", fmt.Sprintf("$%02X", mem.AltROMReg()),
						"bytes", by.String())
				}
				if pc == 0x0808 && cpu.InstructionCount() > 21_000_000 && n090 < 12 {
					n090++
					var tbl strings.Builder
					for a := uint16(0x232B); a <= 0x2345; a++ {
						fmt.Fprintf(&tbl, "%02X ", mem.Read(a))
					}
					slog.Info("cap-0808", "n", n090, "insn", cpu.InstructionCount(),
						"mem_2331", fmt.Sprintf("$%02X", mem.Read(0x2331)),
						"af", fmt.Sprintf("$%04X", uint16(cpu.A)<<8|uint16(cpu.F)),
						"tbl_232B", tbl.String())
				}
				if (pc == 0x090E || pc == 0x090F) && cpu.InstructionCount() > 22_000_000 && n090 < 16 {
					n090++
					slog.Info("cap-090E", "n", n090, "pc", fmt.Sprintf("$%04X", pc),
						"op", fmt.Sprintf("%02X %02X %02X", mem.Read(pc), mem.Read(pc+1), mem.Read(pc+2)),
						"af", fmt.Sprintf("$%04X", uint16(cpu.A)<<8|uint16(cpu.F)),
						"bc", fmt.Sprintf("$%04X", cpu.BC()), "hl", fmt.Sprintf("$%04X", cpu.HL()),
						"memhl", fmt.Sprintf("$%02X", mem.Read(cpu.HL())),
						"rombank", mem.GetROMBank())
				}
				if !dumped && pc == 0x3F44 && cpu.InstructionCount() > 23_000_000 {
					dumped = true
					slog.Info("cap-3f44", "hl", fmt.Sprintf("$%04X", cpu.HL()),
						"iff1", cpu.IFF1, "iff2", cpu.IFF2)
				}
			})
		}
		// ZX_GO_CAPTURE_E37: log the PC trajectory forward from bank-2 $0E37
		// (the NEXTREG $8C,$C0 launcher write) the FIRST time it fires post-
		// launch, to see where our launcher leaves the editor-handoff path
		// (the reference emulator: $0E37 -> $0465/$055D sets NR$8C=$80 -> editor; ours derails).
		if os.Getenv("ZX_GO_CAPTURE_E37") != "" {
			var armed bool
			var n int
			var b strings.Builder
			armPC := uint16(0x0E37)
			armGate := uint64(23_200_000)
			if v := os.Getenv("ZX_GO_CAPTURE_ARM_PC"); v != "" {
				if x, err := strconv.ParseUint(strings.TrimPrefix(v, "0x"), 16, 16); err == nil {
					armPC = uint16(x)
				}
			}
			if v := os.Getenv("ZX_GO_CAPTURE_ARM_GATE"); v != "" {
				if x, err := strconv.ParseUint(v, 10, 64); err == nil {
					armGate = x
				}
			}
			cpu.AddPreFetchHook("capture-e37", func(pc uint16) {
				if !armed {
					if pc == armPC && cpu.InstructionCount() > armGate {
						armed = true
					} else {
						return
					}
				}
				if n < 9000 {
					fmt.Fprintf(&b, "%04X ", pc)
					n++
					if n == 9000 {
						_ = os.WriteFile("/tmp/our_traj.txt", []byte(b.String()+"\n"), 0o644)
						slog.Info("e37-trace", "wrote", "/tmp/our_traj.txt", "steps", n, "nr8c", fmt.Sprintf("$%02X", mem.AltROMReg()))
					}
				}
			})
		}
		// ZX_GO_CAPTURE_HL: at the launcher's filename/dir-parse loop ($0366
		// LD A,(HL)) dump HL + the parsed bytes + IX/DE — the divergent data.
		if os.Getenv("ZX_GO_CAPTURE_HL") != "" {
			var nHL int
			cpu.AddPreFetchHook("capture-hl", func(pc uint16) {
				if pc != 0x0366 || cpu.InstructionCount() < 23_200_000 || nHL >= 6 {
					return
				}
				nHL++
				hl := cpu.HL()
				var bs strings.Builder
				for k := uint16(0); k < 44; k++ {
					fmt.Fprintf(&bs, "%02X ", mem.Read(hl+k))
				}
				slog.Info("capture-hl", "n", nHL,
					"hl", fmt.Sprintf("$%04X", hl), "de", fmt.Sprintf("$%04X", cpu.DE()),
					"ix", fmt.Sprintf("$%04X", cpu.IX), "bc", fmt.Sprintf("$%04X", cpu.BC()),
					"mem_hl", bs.String())
			})
		}
		// ZX_GO_CAPTURE_LAUNCH: capture the NextZXOS ROM3-call trampoline
		// ($3CFC = NEXTREG $8E,$03 -> $3D00 esxDOS $3DXX trap) request regs
		// the first few times it fires AFTER the 128K-BASIC launch (the insn
		// gate skips the identical boot-time hits). Reveals the esxDOS
		// function code + the launcher's poll target.
		if os.Getenv("ZX_GO_CAPTURE_LAUNCH") != "" {
			var nCap int
			cpu.AddPreFetchHook("capture-launch-3cfc", func(pc uint16) {
				if pc != 0x3CFC || cpu.InstructionCount() < 23_500_000 || nCap >= 8 {
					return
				}
				nCap++
				sp := cpu.SP
				stk := make([]string, 6)
				for i := 0; i < 6; i++ {
					stk[i] = fmt.Sprintf("$%04X", uint16(mem.Read(sp+uint16(i*2)))|uint16(mem.Read(sp+uint16(i*2+1)))<<8)
				}
				slog.Info("launch-3cfc",
					"n", nCap, "insn", cpu.InstructionCount(),
					"af", fmt.Sprintf("$%04X", uint16(cpu.A)<<8|uint16(cpu.F)),
					"bc", fmt.Sprintf("$%04X", cpu.BC()),
					"de", fmt.Sprintf("$%04X", cpu.DE()),
					"hl", fmt.Sprintf("$%04X", cpu.HL()),
					"ix", fmt.Sprintf("$%04X", cpu.IX), "iy", fmt.Sprintf("$%04X", cpu.IY),
					"sp", fmt.Sprintf("$%04X", sp), "stack", stk[0]+" "+stk[1]+" "+stk[2]+" "+stk[3]+" "+stk[4]+" "+stk[5])
			})
		}
		go func() {
			tick := time.NewTicker(2 * time.Second)
			defer tick.Stop()
			lastReads := make(map[uint16]uint64)
			lastWrites := make(map[uint16]uint64)
			for range tick.C {
				type kv struct {
					port uint16
					n    uint64
				}
				var rdeltas, wdeltas []kv
				for p, c := range readCounts {
					if d := c - lastReads[p]; d > 0 {
						rdeltas = append(rdeltas, kv{p, d})
					}
					lastReads[p] = c
				}
				for p, c := range writeCounts {
					if d := c - lastWrites[p]; d > 0 {
						wdeltas = append(wdeltas, kv{p, d})
					}
					lastWrites[p] = c
				}
				sort.Slice(rdeltas, func(i, j int) bool { return rdeltas[i].n > rdeltas[j].n })
				sort.Slice(wdeltas, func(i, j int) bool { return wdeltas[i].n > wdeltas[j].n })
				for i, kv := range rdeltas {
					if i >= 10 {
						break
					}
					slog.Info("port-read-trace",
						"port", fmt.Sprintf("$%04X", kv.port), "reads_per_2s", kv.n)
				}
				for i, kv := range wdeltas {
					if i >= 10 {
						break
					}
					slog.Info("port-write-trace",
						"port", fmt.Sprintf("$%04X", kv.port), "writes_per_2s", kv.n)
				}
			}
		}()
	}
	ayEngine := ay.NewEngine()
	if existing := u.AY(); existing != nil {
		ayEngine.SetChip(0, existing)
	}
	l2 := layer2.New(mem)
	pal := palette.NewBank()
	prio := next.NewLayerPriority()
	sprites := sprite.New()
	cop := copper.New()
	cop.SetRegWriter(disp)
	rtcEngine := rtcpkg.New()
	// ZX_GO_RTC_FIXED=<RFC3339> freezes the guest clock — makes the
	// NextZXOS menu's per-second redraw task quiescent so key events
	// deterministically land in the deep-idle context (the engine
	// launch gate reads the SP page).
	if fixed, ok := parseRTCFixed(os.Getenv("ZX_GO_RTC_FIXED")); ok {
		rtcEngine.SetNow(func() time.Time { return fixed })
		slog.Info("rtc frozen", "at", fixed.Format(time.RFC3339))
	}
	// Battery-backed NVRAM persistence: store the DS1307 general-purpose
	// RAM (registers 0x08-0x3F) in the ROM/config dir so it survives
	// across runs. install.Path honours ZX_GO_NEXT_ROM_DIR, so tests
	// using installtest.RedirectConfig stay sandboxed.
	if dir, err := install.Path(); err == nil {
		rtcEngine.SetPersistPath(filepath.Join(dir, "i2c_nvram"))
	}
	uartEngine := uartpkg.New()
	keymapEngine := keymap.New()
	tilemapLayer := tilemap.New(mem)
	dmaEngine := dma.New(mem)
	dacBank := dac.New()
	// divMMC pager built early so we can wire NextReg $0A bit 4 →
	// pager.SetAutomap in next.Wire. ROM is optional.
	divROM, err := install.LoadROM(install.DivMMCROM)
	if err != nil && !errors.Is(err, install.ErrROMNotInstalled) {
		return fmt.Errorf("next: divMMC ROM load: %w", err)
	}
	pager := divmmc.New(divROM)
	mem.SetDivMMCRAM(pager)
	// While the Multiface is the active NMI master, the divMMC must not steal
	// the $0066 NMI vector (the MF handler owns it) — but the divMMC still
	// automaps for the handler's esxDOS RST-$08 calls.
	pager.SetMultifaceActiveFn(mem.MultifaceActive)

	// Next Multiface ROM (enNextMF.rom). The 128K-BASIC launch fires the
	// Multiface NMI (NR$02=$08); the FPGA pages this 8 KB ROM in over
	// $0000-$1FFF so the $0066 NMI handler runs (loads the editor snapshot).
	// wire.go's NR$02 handler calls mem.SetMultifaceActive(true) on the MF
	// NMI; this just installs the ROM image. Optional — if absent, the MF
	// overlay stays disabled and behaviour is unchanged.
	if mfROM, mferr := install.LoadROM(install.MultifaceROM); mferr == nil {
		mem.SetMultifaceROM(mfROM)
		slog.Info("Next Multiface ROM loaded", "size", len(mfROM))
	} else if !errors.Is(mferr, install.ErrROMNotInstalled) {
		return fmt.Errorf("next: Multiface ROM load: %w", mferr)
	}
	if os.Getenv("ZX_GO_DIVMMC_PAGE_TRACE") != "" {
		pager.SetPageLogger(func(event string, pc uint16) {
			slog.Info("divmmc-page", "ev", event, "pc", fmt.Sprintf("$%04X", pc),
				"rom_bank", mem.GetROMBank(),
				"alt", fmt.Sprintf("$%02X", mem.AltROMReg()))
		})
	}

	// divMMC RAM starts UNINITIALISED ($FF — see Pager.New). Do NOT
	// pre-seed bank 0 with the esxDOS image here: real hardware boots
	// with virgin $FF divMMC RAM, and a seed corrupts the per-partition
	// state byte at bank-0 $0301 (the OS expects uninitialised $FF
	// there; a written $08 forces the wrong mount branch and a $3BF5
	// deliberate-reset loop). NextZXOS populates divMMC RAM itself at
	// mount time via its $0703/$0769 cross-bank primitives.

	// Optional: pre-populate divMMC RAM bank 1 from a user-supplied
	// snapshot. On real Spectrum Next, TBBLUE.FW installs an
	// elaborate IRQ stub at $2009 during FPGA configuration; without
	// it, the boot reaches IRQ-idle but the NextZXOS frame handler
	// never gets keyboard scan / autoexec-gate signalling and
	// Browser never renders.
	//
	// Loading is OPT-IN (debug shortcut). Real Spectrum Next has
	// TBBLUE.FW install IRQ stubs into divMMC RAM bank 1 during FPGA
	// configuration; for ANY captured-snapshot-based path to work
	// post-warm-boot, that bank-1 content must be in place. Per the
	// project's "no hacks" rule, this only loads when the user has
	// explicitly opted into the warm-boot path (--warm-boot flag or
	// ZX_GO_WARM_BOOT=1) — otherwise pure cold-boot leaves bank 1
	// at $00, which is the correct hardware-faithful state.
	wantWarmBoot := (cliFlagsActive != nil && cliFlagsActive.warmBoot) ||
		os.Getenv("ZX_GO_WARM_BOOT") != ""
	if !wantWarmBoot {
		// snapshot path disabled — cold boot starts with clean RAM
	} else if snap, err := install.LoadROM(install.DivMMCRAMBank1); err == nil {
		if want := divmmc.BankSize; len(snap) >= want {
			copy(pager.RAMBank(1), snap[:want])
			// Shadow-protect bank 1 $0009 so NextZXOS's later
			// $C9 placeholder write doesn't clobber the IRQ stub
			// byte. Mirrors the FPGA's internal shadow that keeps
			// TBBLUE.FW's installed code effective.
			pager.SetStubProtected(true)
			slog.Info("divMMC RAM bank 1 snapshot loaded",
				"bytes", want,
				"sample_at_$2009", fmt.Sprintf("$%02X", snap[0x09]),
				"stub_protected", true)
		} else {
			slog.Warn("divMMC RAM bank 1 snapshot too small; ignoring",
				"got", len(snap), "want", want)
		}
	} else if !errors.Is(err, install.ErrROMNotInstalled) {
		slog.Warn("divMMC RAM bank 1 snapshot load failed", "err", err)
	}

	// Optional: pre-populate 8K main-RAM bank $DF (= 16K bank 111
	// offset $2000-$3FFF) from a captured snapshot. Real Spectrum Next
	// has TBBLUE.FW pre-load handler code there. Pure cold boot leaves
	// bank $DF empty, which is hardware-faithful (real hardware also
	// has uninit RAM until TBBLUE.FW writes to it). The pre-fill is a
	// DEBUG SHORTCUT — same opt-in gate as the divMMC bank 1 snapshot
	// above.
	if wantWarmBoot {
		if snap, err := install.LoadROM(install.MainRAMBankDF); err == nil {
			const want = 8192
			// Load into BOTH bank 111 high half (= 8K bank $DF, where
			// our boot's slot 7 maps after the memory sweep terminator)
			// AND bank 0 high half (= 8K bank $01, the working-bank
			// mapping seen at every dispatch moment after the sweep on
			// real hardware). Real boot eventually transitions slot 7
			// to bank $01 and continues reading code there, so both
			// need the same post-init content.
			bank111 := mem.GetPage(111)
			bank0 := mem.GetPage(0)
			if len(snap) >= want && len(bank111) >= 0x4000 && len(bank0) >= 0x4000 {
				copy(bank111[0x2000:0x4000], snap[:want])
				copy(bank0[0x2000:0x4000], snap[:want])
				slog.Info("main RAM bank $DF snapshot loaded",
					"bytes", want,
					"sample_at_$ED82",
					fmt.Sprintf("$%02X $%02X $%02X $%02X",
						snap[0x0D82], snap[0x0D83], snap[0x0D84], snap[0x0D85]),
					"banks", "111+0 (= 8K banks $DF and $01)")
			} else {
				slog.Warn("main RAM bank $DF snapshot too small or no banks",
					"snap_size", len(snap), "bank111", len(bank111),
					"bank0", len(bank0))
			}
		} else if !errors.Is(err, install.ErrROMNotInstalled) {
			slog.Warn("main RAM bank $DF snapshot load failed", "err", err)
		}

		// Companion: pre-populate 8K main-RAM bank $DE (= 16K bank 111
		// low half) from the captured bank $00 snapshot. Same opt-in
		// gate as bank $DF above.
		if snap, err := install.LoadROM(install.MainRAMBankDE); err == nil {
			const want = 8192
			bank111 := mem.GetPage(111)
			bank0 := mem.GetPage(0)
			if len(snap) >= want && len(bank111) >= 0x4000 && len(bank0) >= 0x4000 {
				copy(bank111[0x0000:0x2000], snap[:want])
				copy(bank0[0x0000:0x2000], snap[:want])
				slog.Info("main RAM bank $DE snapshot loaded",
					"bytes", want,
					"banks", "111+0 (= 8K banks $DE and $00)")
			}
		} else if !errors.Is(err, install.ErrROMNotInstalled) {
			slog.Warn("main RAM bank $DE snapshot load failed", "err", err)
		}
	} // end of `if wantWarmBoot` wrapping the bank $DF and $DE loaders

	// FPGA-firmware emulation: install a FRAMES-bumper that fires
	// whenever the CPU M1-fetches at divMMC-RAM-bank-1 $2009 with
	// the overlay paged in. This emulates the load-bearing part of
	// the TBBLUE.FW-preinstalled IM-1 handler. See the framesBump
	// comment in divmmc.go Step().
	if os.Getenv("ZX_GO_NO_FRAMESBUMP") == "" {
		pager.SetFramesBumper(func() {
			// FRAMES at $5C78-$5C7A (24-bit). Bump low 16 bits, then
			// MSB on overflow. IY is fixed at $5C3A in 128 BASIC.
			lo := uint16(mem.Read(0x5C78)) | (uint16(mem.Read(0x5C79)) << 8)
			lo++
			mem.Write(0x5C78, byte(lo))
			mem.Write(0x5C79, byte(lo>>8))
			if lo == 0 {
				mem.Write(0x5C7A, mem.Read(0x5C7A)+1)
			}
			// Earlier versions also wrote $80 to $26EC here to force
			// the divMMC ROM's "JP C $1FFC" page-out path, but that
			// drops the overlay regardless of where the interrupted
			// PC was — and on real Spectrum Next the interrupted PC
			// can be inside the overlay (e.g. NEXTBASIC code that's
			// been copied into divMMC RAM and is running with the
			// overlay paged in). Forcing a drop there leaves PC
			// pointed at a $0000-$3FFF address that's no longer
			// readable from the overlay, landing on whatever the
			// underlying ROM bank holds. Empirically that lands at
			// bank-2 $0000 = "NOP; JR -3" — an unreachable infinite
			// loop. So the drop signal stays caller-controlled: only
			// explicit divMMC paths (esxDOS, RST 28 etc.) set $26EC,
			// matching the TBBLUE.FW pre-installed handler's
			// "stay paged in for normal /INT" default.
		})
	}
	// rom3 automap entry points (NR$B9 bit clear) engage per the FULL
	// zxnext.vhd:3138 gate — (altrom_en AND alt_128_n) OR (rom3 AND NOT
	// altrom_en) — not just "ROM3 selected". The altrom arm is
	// load-bearing for the menu-era $0038 IM1 automap: NextZXOS runs the
	// menu with ROM 0 paged but an Alt-ROM read-replacement configuration
	// staged across its boot soft-reset (NR$8C nibble promote), which
	// opens the gate via alt_128_n. A rom_bank==3-only predicate would
	// deny every menu frame INT (rom_bank stays 0 there), starving the
	// OS ISR and stalling the Browser on ENTER.
	pager.SetRom3Query(mem.DivMMCRom3Gate)
	if os.Getenv("ZX_GO_DIVMMC_WRITE_TRACE") != "" {
		var n int
		// Cap controlled by $ZX_GO_DIVMMC_WRITE_TRACE_LIMIT; default 200.
		limit := 200
		if v := os.Getenv("ZX_GO_DIVMMC_WRITE_TRACE_LIMIT"); v != "" {
			if x, err := strconv.Atoi(v); err == nil && x > 0 {
				limit = x
			}
		}
		pager.SetWriteLogger(func(bank int, addr uint16, val byte) {
			if n < limit {
				slog.Info("divmmc-ram-write",
					"bank", bank,
					"addr", fmt.Sprintf("$%04X", addr),
					"val", fmt.Sprintf("$%02X", val),
					"pc", fmt.Sprintf("$%04X", cpu.PC))
			}
			n++
		})
	}
	if os.Getenv("ZX_GO_PORT_E3_DEEP_TRACE") != "" {
		var lastVal uint16
		var writeCount uint64
		var chainWrites uint64
		pager.SetWriteLogger(func(bank int, addr uint16, val byte) {
			// (a) Trace writes to bank-1 $26D1-$26F0 — the divMMC
			// scheduler task-chain region. Any external write
			// here is what could break the infinite loop.
			if bank == 1 && addr >= 0x26D1 && addr <= 0x26F0 {
				chainWrites++
				if chainWrites <= 50 || chainWrites&0xFFF == 0 {
					slog.Info("e3-chain-write",
						"count", chainWrites,
						"addr", fmt.Sprintf("$%04X", addr),
						"val", fmt.Sprintf("$%02X", val),
						"writer_pc", fmt.Sprintf("$%04X", cpu.PC))
				}
			}
			// (b) Trace writes to bank-1 $25B8 — the pivot ptr.
			if bank == 1 && (addr == 0x25B8 || addr == 0x25B9) {
				if addr == 0x25B8 {
					lastVal = (lastVal & 0xFF00) | uint16(val)
				} else {
					lastVal = (lastVal & 0x00FF) | (uint16(val) << 8)
					writeCount++
					if writeCount <= 20 || writeCount&0xFFFF == 0 {
						slog.Info("e3-25B8-write",
							"count", writeCount,
							"new_25B8", fmt.Sprintf("$%04X", lastVal),
							"writer_pc", fmt.Sprintf("$%04X", cpu.PC))
					}
				}
			}
		})
	}
	// Automap starts disabled. enNextZX bank-0 enables it itself
	// at $023E (LD A,$0A; CALL $0D6B; OR $10; OUT(C),A — i.e.
	// select NextReg $0A then set bit 4) during its early boot,
	// once it has run past the reset entry at $0000. Forcing
	// automap=true before CPU start would trigger the divMMC overlay
	// on the very first $0000 M1 fetch (since $0000 ∈ TriggerPCs),
	// hijacking enNextZX's reset entry.
	//
	// Load the FPGA bootrom BEFORE next.Wire: WireReset seeds the
	// NR$02 reset_type history from mem.FPGABootROMActive(), and a
	// real FPGA-bootrom boot powers on at the VHDL "100" ($04) seed.
	// Loading the bootrom after next.Wire would leave that seed at
	// the direct-boot "010" ($02) instead, so the single NextZXOS
	// staging soft reset shifts to the wrong value and the boot
	// skips the 128K-BASIC staging pass — a black screen (a bad RET
	// to $FF00 → NOP-slide → $0000 hang). Loading here makes
	// FPGABootROMActive() true at seed.
	fpgaBootArmed := os.Getenv("ZX_GO_NO_FPGA_BOOTROM") == ""
	if fpgaBootArmed {
		fpgaROM, err := install.LoadROM(install.FPGABootROM)
		switch {
		case err == nil:
			mem.SetFPGABootROM(fpgaROM)
			slog.Info("FPGA bootrom loaded", "size", len(fpgaROM))
		case errors.Is(err, install.ErrROMNotInstalled):
			// LoadROM falls back to the bundled GPLv3 loader, so this
			// branch should be unreachable in a normal build. If it
			// fires, the embedded asset is missing (build problem).
			slog.Warn("next: FPGA loader unavailable (neither installed nor embedded?) — falling back to direct enNextZX boot (will drop to 48K BASIC).")
			fpgaBootArmed = false
		default:
			return fmt.Errorf("next: FPGA bootrom load: %w", err)
		}
	}
	next.Wire(next.WireOpts{
		Dispatcher:  disp,
		Memory:      mem,
		CPU:         cpu,
		AYEngine:    ayEngine,
		Layer2:      l2,
		Palette:     pal,
		Priority:    prio,
		Sprites:     sprites,
		Copper:      cop,
		RTC:         rtcEngine,
		UART:        uartEngine,
		Keymap:      keymapEngine,
		Tilemap:     tilemapLayer,
		DivMMCPager: pager,
	})
	cpu.NextRegs = disp
	u.SetNextRegs(disp)

	// FPGA bootrom mode — the hardware-faithful path. Real Spectrum
	// Next hardware always boots through the FPGA loader at
	// $0000-$3FFF: it runs the initial Z80 reset entry, reads
	// TBBLUE.FW from SD via SPI, copies it to RAM at $6000, then
	// writes NextReg $03 to select the machine personality. This path
	// reaches the NextZXOS Browser splash with the tilemap layer
	// rendering — unlike the "direct enNextZX" fallback path, which
	// drops to 48K BASIC because it bypasses the boot.bin-loaded
	// divMMC modules and the config-mode RAM-bank dispatch.
	//
	// $ZX_GO_NO_FPGA_BOOTROM=1 opts out (booting through the classic
	// ROM banks directly), kept for regression testing / users who
	// specifically want the old behaviour. With no opt-out and no
	// installed tbblue_loader.rom we log a warning and boot the
	// legacy direct path.
	if spec := os.Getenv("ZX_GO_CONFIG_WRITE_TRACE"); spec != "" {
		// spec format: "page:addr_lo-addr_hi" hex (e.g. "03:0000-0017"
		// to log every config-mode write to ROM 3 at $0000-$0017).
		// Skips when no comma-separated page matches or addr is
		// outside the range. Logs page + addr + val + CPU PC.
		var watchPage byte
		var watchLo, watchHi uint16
		if n, _ := fmt.Sscanf(spec, "%02X:%04X-%04X", &watchPage, &watchLo, &watchHi); n == 3 {
			mem.SetConfigWriteHook(func(p byte, addr uint16, val byte) {
				if p == watchPage && addr >= watchLo && addr <= watchHi {
					slog.Info("config-write",
						"page", fmt.Sprintf("$%02X", p),
						"addr", fmt.Sprintf("$%04X", addr),
						"val", fmt.Sprintf("$%02X", val),
						"pc", fmt.Sprintf("$%04X", cpu.PC))
				}
			})
		}
	}
	if os.Getenv("ZX_GO_IFF_TRACE") != "" {
		// Log every DI ($F3) / EI ($FB) M1 fetch with PC and the
		// resulting IFF1 state. Triggered as a pre-fetch hook so we
		// see PC = the DI/EI itself.
		cpu.AddPreFetchHook("iff-trace", func(pc uint16) {
			b := mem.Read(pc)
			if b == 0xF3 || b == 0xFB {
				name := "DI"
				if b == 0xFB {
					name = "EI"
				}
				slog.Info("iff-trace",
					"insn", name,
					"pc", fmt.Sprintf("$%04X", pc),
					"iff1_before", cpu.IFF1,
					"insns", cpu.InstructionCount())
			}
		})
	}
	if os.Getenv("ZX_GO_PAGING_TRACE") != "" {
		// Log every classic-paging port write (7FFD/1FFD) with pc +
		// insn count — including writes DROPPED by the bit-5 lock —
		// plus the +3 special-paging (all-RAM) flag around it. Shows
		// whether the guest's $1FFD special-mode writes are made and
		// honoured.
		mem.SetPagingTracer(func(source string, val byte, applied, specialBefore, specialAfter bool) {
			slog.Info("paging-write", "port", source,
				"val", fmt.Sprintf("$%02X", val),
				"applied", applied,
				"special", fmt.Sprintf("%v->%v", specialBefore, specialAfter),
				"pc", fmt.Sprintf("$%04X", cpu.PC),
				"insn", cpu.InstructionCount())
		})
	}
	if spec := os.Getenv("ZX_GO_RAM_WRITE_TRACE"); spec != "" {
		// spec format: "bank:addr_lo-addr_hi" hex (e.g. "05:2000-2FFF"
		// to log every Memory.Write that lands in 16K SRAM bank 5 at
		// addresses $2000-$2FFF), or "*:addr_lo-addr_hi" to match the
		// offset in EVERY bank — finds which physical bank backs a
		// logical sysvar when the OS remaps the slot (e.g. a
		// continuation that lives in a non-default bank).
		if watchBank, watchLo, watchHi, ok := parseRAMWriteTraceSpec(spec); ok {
			mem.SetRAMWriteHook(func(bank int, addr uint16, val byte) {
				if (watchBank < 0 || bank == watchBank) && addr >= watchLo && addr <= watchHi {
					slog.Info("ram-write",
						"bank", fmt.Sprintf("%d", bank),
						"addr", fmt.Sprintf("$%04X", addr),
						"val", fmt.Sprintf("$%02X", val),
						"pc", fmt.Sprintf("$%04X", cpu.PC))
				}
			})
		}
	}
	if spec := os.Getenv("ZX_GO_DIVMMC_WRITE_TRACE"); spec != "" {
		// Same spec syntax as ZX_GO_RAM_WRITE_TRACE but for divMMC
		// RAM: "B:LLLL-HHHH" (8K bank + WINDOW addresses $2000-$3FFF)
		// or "*:LLLL-HHHH". Finds who populates/corrupts a divMMC cell.
		//
		// Must wire on the in-scope `pager` directly, not via
		// u.NextDivMMC() — that accessor isn't wired until later in
		// this function, so a type assertion against it here would
		// fail silently and the logger would never be installed.
		if watchBank, watchLo, watchHi, ok := parseRAMWriteTraceSpec(spec); ok {
			pager.SetWriteLogger(func(bank int, addr uint16, val byte) {
				if (watchBank < 0 || bank == watchBank) && addr >= watchLo && addr <= watchHi {
					slog.Info("divmmc-write",
						"bank", fmt.Sprintf("%d", bank),
						"addr", fmt.Sprintf("$%04X", addr),
						"val", fmt.Sprintf("$%02X", val),
						"pc", fmt.Sprintf("$%04X", cpu.PC))
				}
			})
		}
	}
	// FPGA bootrom: default ON. This is the *real* Spectrum Next
	// boot path — tbblue_loader.rom does the splash + ROM-load
	// sequence, hands off to NextZXOS via a soft reset, and the
	// final state is the NextZXOS Browser. Per the project's
	// "no hacks" rule, this must be the default.
	//
	// Opt-OUT via $ZX_GO_NO_FPGA_BOOTROM=1 — falls back to the
	// "fast-boot" (skip FPGA bootrom + skip NextZXOS), which
	// drops directly to the 48K BASIC ROM with every Next
	// extension wired. Useful for .NEX testing or as a
	// regression check while debugging the cold-boot path.
	// (The FPGA bootrom is loaded ABOVE, before next.Wire, so WireReset's
	// NR$02 reset_type seed is correct — see the comment there.)
	// Bootrom-clear is handled inside WireMachineType (NextReg $03) —
	// the same hardware event as the config-mode transition.
	if !fpgaBootArmed && os.Getenv("ZX_GO_NEXT_DIRECT_BOOT") != "" {
		// Direct-core boot: no FPGA bootrom, no TBBLUE.FW execution.
		// Seed the post-core-config NextReg personality (captured from
		// a live NextZXOS boot) so NextZXOS init reads the machine it
		// expects and completes setup. The CPU resets straight into
		// enNextZX.rom (preloaded into rom[0..3]).
		applyDirectBootNextRegs(disp)
		slog.Info("next: direct-core boot — seeded post-config NextRegs (no FPGA bootrom / no TBBLUE.FW)")
	}
	u.SetNextAY(ayEngine)
	comp := compositor.New(pal, l2)
	comp.SetSprites(sprites)
	u.SetNextSpritePort(sprites) // port $303B select (write) / status (read)
	comp.SetTilemap(tilemapLayer)
	comp.SetPrioritySource(prio)
	u.SetNextCompositor(comp)
	// Give the compositor the ULA's 16-colour palette so it can resolve
	// the ULA transparency colour for the SUL per-pixel stencil + the
	// NR$4A fallback (a transparent ULA pixel carries u.palette[NR$14]).
	comp.SetULAPalette(u.Palette())
	// Wire the live NR$4C (tilemap transparency nibble) into the
	// compositor: a tilemap pixel is transparent when its palette
	// index's low nibble == NR$4C(3:0) (FPGA tilemap.vhd:427). NextZXOS
	// dot commands set this (NextGuide uses $08); this replaces the plain
	// store from the general wiring so the compositor tracks the value —
	// without it the $0F default makes their text transparent and the ULA
	// bleeds through.
	disp.SetOnWrite(0x4C, func(d *nextregs.Dispatcher, val byte) {
		d.Store(0x4C, val&0x0F)
		comp.SetTilemapTransparency(val & 0x0F)
	})
	// NextReg $1E/$1F (active video line MSB/LSB) — a LIVE raster-line
	// counter derived from the CPU T-state position. NextZXOS dot
	// commands (NextGuide) disable interrupts and poll it to wait for the
	// raster; without a live value the wait loop hangs forever.
	disp.SetOnRead(0x1F, func(*nextregs.Dispatcher) byte { return byte(u.ActiveVideoLine() & 0xFF) })
	disp.SetOnRead(0x1E, func(*nextregs.Dispatcher) byte { return byte((u.ActiveVideoLine() >> 8) & 0x01) })
	// Tilemap pixel scroll (NR$2F:$30 = X 10-bit, NR$31 = Y 8-bit) per
	// FPGA nr_30_tm_scrollx / nr_31_tm_scrolly.
	disp.SetOnWrite(0x2F, func(d *nextregs.Dispatcher, val byte) {
		d.Store(0x2F, val&0x03)
		tilemapLayer.SetScrollX(int(val&0x03)<<8 | int(d.ReadReg(0x30)))
	})
	disp.SetOnWrite(0x30, func(d *nextregs.Dispatcher, val byte) {
		d.Store(0x30, val)
		tilemapLayer.SetScrollX(int(d.ReadReg(0x2F)&0x03)<<8 | int(val))
	})
	disp.SetOnWrite(0x31, func(d *nextregs.Dispatcher, val byte) {
		d.Store(0x31, val)
		tilemapLayer.SetScrollY(int(val))
	})
	// NR$14 global transparency index (Layer 2 see-through; default $E3).
	disp.SetOnWrite(0x14, func(d *nextregs.Dispatcher, val byte) {
		d.Store(0x14, val)
		comp.SetTransparency(val)
	})
	// NR$4A fallback colour (RGB332, FPGA nr_4a_fallback_rgb, default $E3)
	// — shown where every layer is transparent at a pixel. Expand RGB332
	// to RGB888 for the compositor.
	expandRGB332 := func(val byte) (byte, byte, byte) {
		r3, g3, b2 := (val>>5)&7, (val>>2)&7, val&3
		return r3<<5 | r3<<2 | r3>>1, g3<<5 | g3<<2 | g3>>1, b2<<6 | b2<<4 | b2<<2 | b2
	}
	disp.SetOnWrite(0x4A, func(d *nextregs.Dispatcher, val byte) {
		d.Store(0x4A, val)
		comp.SetFallbackColour(expandRGB332(val))
	})
	comp.SetFallbackColour(expandRGB332(0xE3)) // power-on default
	// NR$4B sprite transparency index (default $E3). A sprite pixel equal to
	// this index is see-through — how 8bpp sprites (e.g. Nextoid's bat/HUD)
	// mark their transparent cells. The compositor defaults to $E3 too, so a
	// game that never writes NR$4B still gets the correct reset behaviour.
	disp.SetOnWrite(0x4B, func(d *nextregs.Dispatcher, val byte) {
		d.Store(0x4B, val)
		comp.SetSpriteTransparency(val)
	})
	// NR$68 ("ULA Control") bit 7 = Disable ULA output. When set, the ULA
	// layer paints nothing (lower layers / NR$4A fallback show). Sonic
	// disables the ULA for its Layer-2/tilemap title; without this its stale
	// screen RAM rendered as a garbled background.
	disp.SetOnWrite(0x68, func(d *nextregs.Dispatcher, val byte) {
		d.Store(0x68, val)
		u.SetULAOutputDisabled(val&0x80 != 0)
	})
	u.SetNextDMA(dmaEngine)
	// zxnDMA IO endpoints: a port configured as an IO endpoint (WR1/WR2 D3)
	// transfers to/from a real port — sprite-image ($5B), Layer 2 ($253B),
	// DAC, etc. Route those through the ULA's port dispatch.
	dmaEngine.SetIOBus(dmaPortBus{u})
	// Charge a continuous-mode transfer's T-state duration to the CPU clock so
	// the DMA stalls the CPU for the right time (per-byte prescaler + cycle
	// lengths). Burst mode is not charged (the CPU runs during the waits).
	dmaEngine.SetCycleSink(func(n uint64) { cpu.SetTstates(cpu.Tstates() + n) })
	// Burst-mode + prescaler transfers interleave with the CPU: the DMA pumps
	// one byte every prescaler T-states from this per-instruction Step, so
	// DMA-streamed audio is paced across the CPU timeline (and the CPU runs in
	// the gaps). No-op unless such a transfer is in flight.
	dmaEngine.SetClock(func() uint64 { return cpu.Tstates() })
	cpu.AddPreFetchHook("zxndma-step", func(uint16) { dmaEngine.Step(cpu.Tstates()) })
	// i2c DS1307 RTC on ports $103B/$113B (zxnext.vhd:2630/3234) —
	// NextZXOS bit-bangs this bus for the menu's date/time line; with
	// no slave the clock fetch fails every frame and the menu engine
	// degenerates into a re-render storm.
	u.SetNextI2C(rtcpkg.NewBus(rtcEngine))
	u.SetNextCopper(cop)
	u.SetNextDAC(dacBank)

	// divMMC pager was constructed above and wired through
	// next.Wire so NextReg $0A bit 4 toggles its automap state.
	// Hook it onto the CPU pre-fetch path and the ULA dispatcher.
	cpu.AddPreFetchHook("divmmc", pager.Step)
	if os.Getenv("ZX_GO_DIVMMC_PAGE_TRACE") != "" {
		// Log every automap latch transition with pc + insn count —
		// the latch timeline is the ground truth for dispatch-era
		// overlay state.
		pager.SetPageLogger(func(event string, pc uint16) {
			slog.Info("divmmc-page", "event", event,
				"pc", fmt.Sprintf("$%04X", pc),
				"insn", cpu.InstructionCount())
		})
	}
	// ZX_GO_CAPTURE_LOOP: once the instruction count passes a gate (deep
	// in the post-launch 128K-BASIC hang), dump N instructions to a file
	// with the CORRECT opcode (this hook is registered AFTER "divmmc", so
	// pager.Step has already applied the overlay for this PC) plus the
	// live divMMC paged/E3 state and registers. This is the unambiguous
	// loop trace the E37 capture can't give (its mem.Read predates paging).
	if g := os.Getenv("ZX_GO_CAPTURE_LOOP"); g != "" {
		gate := uint64(27_000_000)
		if v, err := strconv.ParseUint(g, 10, 64); err == nil && v > 1 {
			gate = v
		}
		var b strings.Builder
		var n int
		const maxN = 5000
		cpu.AddPreFetchHook("capture-loop", func(pc uint16) {
			if n >= maxN || cpu.InstructionCount() < gate {
				return
			}
			paged := 0
			if pager.IsPagedIn() {
				paged = 1
			}
			fmt.Fprintf(&b, "%04X %02X p%d e3=%02X 8c=%02X af=%04X hl=%04X de=%04X bc=%04X sp=%04X\n",
				pc, mem.Read(pc), paged, pager.LastE3(), mem.AltROMReg(),
				uint16(cpu.A)<<8|uint16(cpu.F), cpu.HL(), cpu.DE(), cpu.BC(), cpu.SP)
			n++
			if n == maxN {
				_ = os.WriteFile("/tmp/loop_trace.txt", []byte(b.String()), 0o644)
				if b1 := pager.RAMBank(1); b1 != nil {
					_ = os.WriteFile("/tmp/divmmc_bank1.bin", b1, 0o644)
				}
				if b3 := pager.RAMBank(3); b3 != nil {
					_ = os.WriteFile("/tmp/divmmc_bank3.bin", b3, 0o644)
				}
				if b0 := pager.RAMBank(0); b0 != nil {
					_ = os.WriteFile("/tmp/divmmc_bank0.bin", b0, 0o644)
				}
				rd16 := func(a uint16) uint16 { return uint16(mem.Read(a)) | uint16(mem.Read(a+1))<<8 }
				var sb strings.Builder
				for a := uint16(0x5B40); a < 0x5C02; a += 2 {
					fmt.Fprintf(&sb, "%04X=%04X ", a, rd16(a))
				}
				slog.Info("capture-loop-sysvars",
					"5B52", fmt.Sprintf("$%04X", rd16(0x5B52)),
					"5B56", fmt.Sprintf("$%04X", rd16(0x5B56)),
					"25B7", fmt.Sprintf("$%02X", mem.Read(0x25B7)),
					"25B8", fmt.Sprintf("$%04X", rd16(0x25B8)),
					"region5B", sb.String())
				slog.Info("capture-loop", "wrote", "/tmp/loop_trace.txt", "n", n, "gate", gate)
			}
		})
	}
	// RETN/RETI clear the automap latch on real hardware (the T80N
	// asserts I_RETN for both; zxnext.vhd feeds it as i_retn_seen).
	// RETN (ED 45) pages out BOTH the divMMC and the Next Multiface
	// (zxnext changelog 3.01.09 — "executing a retn will now disable the
	// multiface and the divmmc"). The MF $0066 handler ends with a RETN to
	// return to the interrupted program; that same RETN pages the MF out.
	cpu.SetRETNHook(func() {
		if mem.MultifaceActive() {
			mem.SetMultifaceActive(false)
		}
		pager.HandleRETN()
	})
	// PostStep applies the $1FF8-$1FFF page-out AFTER the M1 fetch
	// completes — matching FPGA timing where automap_held updates on
	// the rising edge of MREQ at end-of-M1. The $1FFC opcode must
	// still come from divMMC ROM (the IRQ tail $E1 = POP HL);
	// dropping pagedIn pre-fetch would corrupt it.
	cpu.AddPostFetchHook("divmmc-pageout", pager.PostStep)
	u.SetNextDivMMC(pager)

	// Build a virtual SD card from the distro directory under
	// roms/next/sd. Empty/missing root logs a warning and the boot
	// proceeds without media; NextZXOS will stall at the SD probe
	// in that case.
	//
	// $ZX_GO_NEXT_SD_IMG overrides the in-memory FAT16 build with a
	// host-side.img/.mmc file (handy for booting the bootrom against
	// a known-good the reference Spectrum Next emulator-style image).
	if imgPath := install.SDCardImage(); imgPath != "" {
		raw, err := os.ReadFile(imgPath)
		if err != nil {
			slog.Warn("next: SD image file read failed", "path", imgPath, "err", err)
		} else {
			src, _ := sdcard.NewImageSource(raw, false)
			// Expose the live in-memory SD image so File->Open .nex can
			// import a copy into it (confirmImportNex) — the guest reads
			// the same backing bytes. This is required for .nex loading to
			// be allowed at all (the load path gates on sdImageSrc != nil).
			e.sdImageSrc = src
			// Opt-in cross-process persistence (--sd-writeback):
			// remember the host path so flushSDWriteback can persist
			// guest writes at exit (with a .bak backup). Default OFF —
			// in-session writes (incl. imported .nex) live in RAM only.
			if cliFlagsActive != nil && cliFlagsActive.sdWriteback {
				e.sdImagePath = imgPath
			}
			card := sdcard.NewCard(src)
			sdDebug := os.Getenv("ZX_GO_SD_DEBUG") != "" || (cliFlagsActive != nil && cliFlagsActive.logSDCommands)
			card.SetLogger(func(cmd byte, arg uint32, isACMD bool) {
				// SD-command breakpoint (`break-on-sd`): pause the CPU when
				// a matching command is issued. nil-safe — rdbg is assigned
				// after wiring, so it is read live on each command.
				e.rdbg.checkSDCommand(cmd, arg, isACMD)
				if sdDebug {
					tag := "CMD"
					if isACMD {
						tag = "ACMD"
					}
					slog.Debug("sd-cmd", "op", fmt.Sprintf("%s%d", tag, cmd), "arg", fmt.Sprintf("$%08X", arg), "pc", fmt.Sprintf("$%04X", cpu.PC),
						"hl", fmt.Sprintf("$%04X", cpu.HL()), "de", fmt.Sprintf("$%04X", cpu.DE()), "bc", fmt.Sprintf("$%04X", cpu.BC()), "a", fmt.Sprintf("$%02X", cpu.A))
				}
			})
			if os.Getenv("ZX_GO_SD_BYTE_TRACE") != "" {
				card.SetByteLogger(func(write bool, val byte) {
					dir := "RD"
					if write {
						dir = "WR"
					}
					slog.Debug("sd-byte",
						"dir", dir,
						"val", fmt.Sprintf("$%02X", val),
						"insn", cpu.InstructionCount(),
						"pc", fmt.Sprintf("$%04X", cpu.PC))
				})
			}
			// Default to SDHC (block-addressed) — matches real Next
			// SD cards and the FPGA bootrom's expectations. Override
			// via $ZX_GO_NEXT_SDSC=1 for legacy SDSC byte-addressing
			// testing.
			if os.Getenv("ZX_GO_NEXT_SDSC") == "" {
				card.SetSDHC(true)
			}
			pager.SetCard(card)
			slog.Info("SD card loaded from image", "path", imgPath, "size", len(raw))
			goto sdReady
		}
	}
	if root := install.SDCardRoot(); root != "" {
		// FAT32-LBA is the bootable format: NextZXOS requires it, and
		// a FAT32 image built from the host tree boots to the welcome
		// screen and launches menu items. A FAT16 image does not boot.
		img, err := sdcard.BuildFAT32(root, sdcard.FAT32Opts{
			SizeMB:      256,
			VolumeLabel: "ZXNEXT",
			SkipFile:    sdcard.NextBootFilter(),
		})
		if err != nil {
			slog.Warn("next: SD card image build failed; booting without SD card", "root", root, "err", err)
		} else {
			src, _ := sdcard.NewImageSource(img, false)
			// Expose the live in-memory FAT32 image (built from the host
			// directory) so File->Open .nex can import into it — the guest
			// reads the same backing bytes. Without this, folder mode (the
			// default, roms/next/sd) left sdImageSrc nil and every GUI .nex
			// load was wrongly blocked as "no SD card configured". Folder
			// mode has no single host file to write back to, so sdImagePath
			// stays empty (imports live in RAM for the session).
			e.sdImageSrc = src
			card := sdcard.NewCard(src)
			sdDebug := os.Getenv("ZX_GO_SD_DEBUG") != "" || (cliFlagsActive != nil && cliFlagsActive.logSDCommands)
			card.SetLogger(func(cmd byte, arg uint32, isACMD bool) {
				// SD-command breakpoint (`break-on-sd`): pause the CPU when
				// a matching command is issued. nil-safe — rdbg is assigned
				// after wiring, so it is read live on each command.
				e.rdbg.checkSDCommand(cmd, arg, isACMD)
				if sdDebug {
					tag := "CMD"
					if isACMD {
						tag = "ACMD"
					}
					slog.Debug("sd-cmd", "op", fmt.Sprintf("%s%d", tag, cmd), "arg", fmt.Sprintf("$%08X", arg), "pc", fmt.Sprintf("$%04X", cpu.PC),
						"hl", fmt.Sprintf("$%04X", cpu.HL()), "de", fmt.Sprintf("$%04X", cpu.DE()), "bc", fmt.Sprintf("$%04X", cpu.BC()), "a", fmt.Sprintf("$%02X", cpu.A))
				}
			})
			if os.Getenv("ZX_GO_SD_BYTE_TRACE") != "" {
				card.SetByteLogger(func(write bool, val byte) {
					dir := "RD"
					if write {
						dir = "WR"
					}
					slog.Debug("sd-byte",
						"dir", dir,
						"val", fmt.Sprintf("$%02X", val),
						"insn", cpu.InstructionCount(),
						"pc", fmt.Sprintf("$%04X", cpu.PC))
				})
			}
			// Default to SDHC (block-addressed) — matches real Next
			// SD cards and the FPGA bootrom's expectations. Override
			// via $ZX_GO_NEXT_SDSC=1 for legacy SDSC byte-addressing
			// testing.
			if os.Getenv("ZX_GO_NEXT_SDSC") == "" {
				card.SetSDHC(true)
			}
			pager.SetCard(card)
		}
	} else {
		slog.Warn("next: no SD card root (roms/next/sd is empty or absent); NextZXOS boot will stall at SD probe")
	}
sdReady:

	esx := esxdos.New()
	esx.SetRTC(rtcEngine)
	// Wire the host-directory SD-card mount through the esxDOS
	// dispatcher too. NEXTBASIC's LOAD "c:/path" syntax routes
	// through esxDOS F_OPEN/F_READ rather than the divMMC raw
	// SPI bus; without a mount here, every LOAD command in
	// running BASIC errors out with "File not found".
	//
	// ONLY in host-dir mode: when a raw SD-card IMAGE is configured
	// the guest's own divMMC/+3DOS code does all filesystem work
	// against the image, and this host shim must stay out of the way
	// (see useESXDOSHostHook) — mixing the two makes the shim answer
	// opens from the host dir while the guest filesystem lives on the
	// image, so a source-open through the guest's own FS code fails
	// on the mismatch.
	hostHook := useESXDOSHostHook(install.SDCardImage(), install.SDCardRoot())
	if hostHook {
		if mount, err := sdcard.NewHostDir(install.SDCardRoot()); err != nil {
			slog.Warn("next: esxDOS host mount failed", "root", install.SDCardRoot(), "err", err)
		} else {
			esx.SetMount(mount)
		}
	}
	// Opt-out: skipping the esxDOS RST 8 hook lets the divMMC ROM's
	// own FAT16 code at enNxtmmc.rom $1500-$1700 run instead. That
	// code maintains filesystem state variables at divMMC RAM bank-N
	// $0009/$000B (the load-bearing values the IRQ handler at $004B
	// reads via `LD HL,($2CF0)` etc.). Our host-side esxDOS handler
	// is far faster but bypasses these writes, leaving the IRQ
	// handler's expected state empty. Set $ZX_GO_DISABLE_ESXDOS_HOOK=1
	// to disable the hook and let the divMMC ROM do real FAT16 work.
	// .NEX games still work because they bypass the OS file path
	// entirely.
	switch {
	case !hostHook:
		slog.Info("next: esxDOS RST 8 host shim not installed (SD image mode — guest FS code handles all file work)")
	case os.Getenv("ZX_GO_DISABLE_ESXDOS_HOOK") != "":
		slog.Info("next: esxDOS RST 8 hook DISABLED via ZX_GO_DISABLE_ESXDOS_HOOK")
	default:
		cpu.AddPreFetchHook("esxdos", esx.HookFunc(cpu, mem, pager))
	}
	// Surface every esxDOS API call at debug level so the post-soft-
	// reset Browser-load investigation can see which files NextZXOS
	// actually touches (vs the strings it just references).
	esx.SetTrace(func(api byte, c *z80.CPU, m esxdos.Memory) {
		var path string
		if api == esxdos.F_OPEN || api == esxdos.F_OPENDIR {
			var sb strings.Builder
			for i := uint16(0); i < 256; i++ {
				b := m.Read(c.HL() + i)
				if b == 0 || b == 0xFF {
					break
				}
				sb.WriteByte(b)
			}
			path = sb.String()
		}
		slog.Debug("esxdos-api",
			"api", fmt.Sprintf("$%02X", api),
			"pc", fmt.Sprintf("$%04X", c.PC),
			"hl", fmt.Sprintf("$%04X", c.HL()),
			"path", path,
		)
	})

	// Chain peripheral hooks: divMMC overlay takes priority (it
	// shadows the bottom 16 KB when paged in), then the existing
	// PeripheralManager.
	mem.PeripheralRead = func(addr uint16) (byte, bool) {
		if val, ok := pager.HandleRead(addr); ok {
			return val, true
		}
		return pm.HandleMemoryRead(addr)
	}
	mem.PeripheralWrite = func(addr uint16, val byte) bool {
		if pager.HandleWrite(addr, val) {
			return true
		}
		return pm.HandleMemoryWrite(addr, val)
	}

	// NMI: on Next, NextZXOS owns the vector by default. Always
	// flag a PendingNMI on keyboard event; the CPU NMI handler
	// then either dispatches to Multiface (if enabled) or lets
	// the running OS service it at $0066.
	kbd.SetNMICallback(func() { cpu.PendingNMI.Store(true) })
	cpu.NMICallback = func() {
		if pm.IsMultifaceEnabled() {
			pm.HandleNMI()
		}
	}

	e.nextEsxdos = esx
	e.nextDAC = dacBank
	e.nextRegs = disp
	e.nextPalette = pal
	e.nextTilemap = tilemapLayer
	e.nextCopper = cop
	e.nextSprites = sprites
	e.nextLayer2 = l2

	// Warm-boot: skip the cold-boot path entirely and load a captured
	// post-init state directly into CPU/RAM/NextRegs. This is a DEBUG
	// SHORTCUT that bypasses the real FPGA-bootrom → TBBLUE.FW →
	// NextZXOS chain. Per the project's "no hacks" rule, it is
	// OPT-IN, never the default, even when the snapshot files are
	// installed.
	//
	// Requires:
	//   roms/next/zes_full_ram.bin  (2 MB Machine RAM dump)
	//   roms/next/zes_nextregs.txt  (256 NR values + CPU regs)
	// captured via ZRCP (save-binary + tbblue-get-register loop).
	//
	// Triggers (any of):
	//   - $ZX_GO_WARM_BOOT=1       — explicit env opt-in (for the
	//                                fyne app and one-off runs)
	//   - --warm-boot CLI flag     — explicit CLI opt-in
	//
	// $ZX_GO_NO_WARM_BOOT=1 is no longer needed (warm-boot is off
	// by default) but is honoured for backward compatibility.
	warmBootForced := os.Getenv("ZX_GO_WARM_BOOT") != "" ||
		(cliFlagsActive != nil && cliFlagsActive.warmBoot)
	if os.Getenv("ZX_GO_NO_WARM_BOOT") != "" {
		warmBootForced = false
	}
	if warmBootForced {
		if err := applyWarmBoot(cpu, mem, disp); err != nil {
			slog.Warn("warm-boot failed", "err", err,
				"trigger", "explicit opt-in")
		} else {
			slog.Info("warm-boot applied — CPU/RAM/NextRegs set to captured post-init state. " +
				"This is a DEBUG SHORTCUT, not a real Spectrum Next boot. " +
				"Disable with ZX_GO_NO_WARM_BOOT=1 or simply omit the opt-in.")
		}
	}
	return nil
}

// unwireNextSubsystems strips the Next bus off an emulator,
// restoring the classic-model state. Called by switchModel when
// the user picks a classic model while currently running on
// ModelNext.
//
// Symmetric with wireNextSubsystems — every install action there
// has a reverse here. Importantly: the audio system stays running
// (SetNextDAC(nil) just detaches the mixer source), and the
// PeripheralManager is preserved so per-peripheral state survives
// the transition.
// disableClassicBusPeripherals tears down the edge-connector
// peripherals that do not exist on Spectrum Next hardware and whose
// port decodes clash with the Next's I/O space — DISCiPLE (control
// port $1F shadows the Kempston read the TBBLUE firmware polls at
// boot), Multiface, and Interface 1. Called when ENTERING the Next
// at runtime, mirroring the cold-boot config-restore gate
// (classicPeripheralsOK). Without it a Machine→Next switch from a
// session that had DISCiPLE enabled wedges the firmware into a
// DI/HALT (an uninitialised-RAM-looking screen on switch). Also
// clears the CPU's DISCiPLE pre/post-fetch hooks, which live in
// dedicated fields separate from the Next's named hook map.
func disableClassicBusPeripherals(e *emulator) {
	pm := e.peripherals
	if pm.IsDiscipleEnabled() {
		pm.DisableDisciple()
	}
	if pm.IsMultifaceEnabled() {
		pm.DisableMultiface()
	}
	if pm.IsInterface1Enabled() {
		pm.DisableInterface1()
	}
	e.cpu.PreFetchHook = nil
	e.cpu.PostFetchHook = nil
}

func unwireNextSubsystems(e *emulator) {
	cpu, mem, u, kbd, pm := e.cpu, e.mem, e.ula, e.kbd, e.peripherals

	cpu.Variant = z80.VariantZ80
	cpu.NextRegs = nil
	cpu.RemovePreFetchHook("divmmc")
	cpu.RemovePreFetchHook("esxdos")

	u.SetNextRegs(nil)
	u.SetNextAY(nil)
	u.SetNextCompositor(nil)
	u.SetNextDMA(nil)
	u.SetNextI2C(nil)
	u.SetNextCopper(nil)
	u.SetNextDivMMC(nil)
	u.SetNextDAC(nil) // also detaches DAC from the running audio mixer

	// Restore the classic peripheral-only memory hooks.
	mem.PeripheralRead = pm.HandleMemoryRead
	mem.PeripheralWrite = pm.HandleMemoryWrite

	// Restore the classic NMI policy: only fire on Multiface
	// enable. This matches newEmulator's setup so the behaviour
	// is identical to a cold-started classic model.
	kbd.SetNMICallback(func() {
		if pm.IsMultifaceEnabled() {
			cpu.PendingNMI.Store(true)
		}
	})
	cpu.NMICallback = func() {
		if pm.IsMultifaceEnabled() {
			pm.HandleNMI()
		}
	}

	e.nextEsxdos = nil
	e.nextDAC = nil
	e.nextRegs = nil
	e.nextPalette = nil
	e.nextTilemap = nil
}

// parseRAMWriteTraceSpec parses ZX_GO_RAM_WRITE_TRACE. Formats:
// "BB:LLLL-HHHH" (hex bank + offset range within the 16K bank) or
// "*:LLLL-HHHH" (all banks; returned bank is -1).
func parseRAMWriteTraceSpec(spec string) (bank int, lo, hi uint16, ok bool) {
	if len(spec) > 2 && spec[0] == '*' && spec[1] == ':' {
		if n, _ := fmt.Sscanf(spec[2:], "%04X-%04X", &lo, &hi); n == 2 {
			return -1, lo, hi, true
		}
		return 0, 0, 0, false
	}
	if n, _ := fmt.Sscanf(spec, "%02X:%04X-%04X", &bank, &lo, &hi); n == 3 {
		return bank, lo, hi, true
	}
	return 0, 0, 0, false
}

// useESXDOSHostHook decides whether the host-directory esxDOS RST 8
// shim should be wired. True ONLY in host-dir SD mode: with a raw SD
// image configured the guest's own divMMC/+3DOS code is the single
// source of filesystem truth (matching real hardware), and the shim
// would create a split-brain filesystem.
func useESXDOSHostHook(sdImage, sdRoot string) bool {
	return sdImage == "" && sdRoot != ""
}

// parseRTCFixed parses ZX_GO_RTC_FIXED (RFC3339). ok=false when
// unset or malformed (malformed logs a warning).
func parseRTCFixed(v string) (time.Time, bool) {
	if v == "" {
		return time.Time{}, false
	}
	tm, err := time.Parse(time.RFC3339, v)
	if err != nil {
		slog.Warn("ZX_GO_RTC_FIXED: bad RFC3339; ignoring", "value", v, "err", err)
		return time.Time{}, false
	}
	return tm, true
}
