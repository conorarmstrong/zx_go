package testharness

import (
	"errors"
	"fmt"
	"log"
	"os"

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
	"github.com/conorarmstrong/zx_go/pkg/next/layer2"
	"github.com/conorarmstrong/zx_go/pkg/next/nex"
	"github.com/conorarmstrong/zx_go/pkg/next/nextregs"
	"github.com/conorarmstrong/zx_go/pkg/next/palette"
	rtcpkg "github.com/conorarmstrong/zx_go/pkg/next/rtc"
	"github.com/conorarmstrong/zx_go/pkg/next/sdcard"
	"github.com/conorarmstrong/zx_go/pkg/next/sprite"
	uartpkg "github.com/conorarmstrong/zx_go/pkg/next/uart"
	"github.com/conorarmstrong/zx_go/pkg/peripherals"
	"github.com/conorarmstrong/zx_go/pkg/roms"
	"github.com/conorarmstrong/zx_go/pkg/ula"
	"github.com/conorarmstrong/zx_go/pkg/z80"
)

// newNext constructs a test harness configured for the ZX Spectrum
// Next: Z80N CPU, NextReg dispatcher routing through the ULA's
// port 0x243B / 0x253B handlers, divMMC auto-pager hooked on M1
// fetches, esxDOS RST 8 trap on the same hook chain, and a
// 48K-style memory map with the user-installed distro ROM.
//
// Returns a wrapped install.ErrROMNotInstalled when the distro ROM
// hasn't been installed yet — call install.InstallROM first via
// the File → Install Next ROMs… UI, or write a fake ROM into
// install.Path() / install.DistroROM for tests.
//
// The divMMC ROM is optional: if divMMC.rom is not installed the
// pager is wired with a nil ROM, which behaves as open bus for any
// fetch from 0x2000–0x3FFF while paged in. The boot path doesn't
// touch the divMMC ROM area, so this is fine.
func newNext() (*Harness, error) {
	mem, err := memory.New("", roms.ModelNext)
	if err != nil {
		return nil, fmt.Errorf("testharness: %w", err)
	}

	kbd := keyboard.New()
	u := ula.New(mem, kbd)
	cpu := z80.New(mem, u)
	cpu.Variant = z80.VariantZ80N

	disp := nextregs.New()
	ayEngine := ay.NewEngine()
	// Adopt the ULA's existing AY as the engine's chip 0 so
	// ModelNext doesn't carry a wasted second instance for that
	// slot. Chips 1 and 2 are fresh.
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
	uartEngine := uartpkg.New()
	dmaEngine := dma.New(mem)
	dacBank := dac.New()
	next.Wire(next.WireOpts{
		Dispatcher: disp,
		Memory:     mem,
		CPU:        cpu,
		AYEngine:   ayEngine,
		Layer2:     l2,
		Palette:    pal,
		Priority:   prio,
		Sprites:    sprites,
		Copper:     cop,
		RTC:        rtcEngine,
		UART:       uartEngine,
	})
	cpu.NextRegs = disp
	u.SetNextRegs(disp)
	u.SetNextAY(ayEngine)
	comp := compositor.New(pal, l2)
	comp.SetSprites(sprites)
	comp.SetPrioritySource(prio)
	u.SetNextCompositor(comp)
	u.SetNextSpritePort(sprites) // port $303B select (write) / status (read); $5B/$57 upload
	u.SetNextDMA(dmaEngine)
	u.SetNextCopper(cop)
	u.SetNextDAC(dacBank)
	// i2c DS1307 RTC on ports $103B/$113B: NextZXOS bit-bangs this bus
	// for the menu's date/time line, so the harness needs a slave to
	// ACK or a real boot's clock read hangs.
	u.SetNextI2C(rtcpkg.NewBus(rtcEngine))

	// divMMC ROM is optional — the boot path doesn't read from
	// the divMMC ROM area on a fresh harness. But ONLY the
	// "not installed" sentinel should be silent; a real I/O
	// error (permission denied, disk full, etc.) is surfaced so
	// the user knows something's wrong with the install dir.
	divROM, err := install.LoadROM(install.DivMMCROM)
	if err != nil && !errors.Is(err, install.ErrROMNotInstalled) {
		return nil, fmt.Errorf("testharness: divMMC ROM load: %w", err)
	}
	pager := divmmc.New(divROM)
	cpu.AddPreFetchHook("divmmc", pager.Step)
	u.SetNextDivMMC(pager)

	if os.Getenv("ZX_GO_SKIP_FPGA_BOOTROM") == "" {
		// Disk-only: the harness boots the FPGA chain only with a
		// genuinely-installed loader (the boot-integration tests),
		// NOT the bundled GPL fallback — so video/unit tests that
		// don't install ROMs keep their hand-set state.
		fpgaROM, err := install.LoadInstalledROM(install.FPGABootROM)
		if err == nil {
			mem.SetFPGABootROM(fpgaROM)
		} else if !errors.Is(err, install.ErrROMNotInstalled) {
			return nil, fmt.Errorf("testharness: FPGA bootrom load: %w", err)
		}
	}

	// Mount a virtual SD card built from the official distro tree
	// (if installed). Without this the divMMC ROM's SD probe times
	// out and NextZXOS never finds its system files.
	if root := install.SDCardRoot(); root != "" {
		// The NextZXOS bootrom expects a FAT32-LBA card.
		img, err := sdcard.BuildFAT32(root, sdcard.FAT32Opts{
			SizeMB:      256,
			VolumeLabel: "ZXNEXT",
			SkipFile:    sdcard.NextBootFilter(),
		})
		if err != nil {
			log.Printf("testharness: BuildFAT32(%s): %v — booting without SD card", root, err)
		} else {
			src, _ := sdcard.NewImageSource(img, false)
			pager.SetCard(sdcard.NewCard(src))
		}
	}

	esx := esxdos.New()
	esx.SetRTC(rtcEngine)
	// Wire the host-directory SD-card mount through esxDOS so
	// NEXTBASIC's LOAD "c:/..." syntax resolves files from the
	// configured SD root. Tests can still override via h.MountSDCard.
	if root := install.SDCardRoot(); root != "" {
		if mount, err := sdcard.NewHostDir(root); err == nil {
			esx.SetMount(mount)
		}
	}
	cpu.AddPreFetchHook("esxdos", esx.HookFunc(cpu, mem, pager))

	pm := peripherals.NewPeripheralManager(mem, "")
	u.SetPeripherals(pm)

	// Chain the peripheral hooks: divMMC overlay takes priority
	// (it shadows the bottom 16 KB when paged in), then anything
	// the classic PeripheralManager wants to handle. The
	// PeripheralManager currently has no Next-relevant peripherals
	// but the chain is here so Multiface Next or future Next-
	// specific peripherals slot in without overwriting divMMC.
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

	return &Harness{
		cpu:         cpu,
		mem:         mem,
		ula:         u,
		kbd:         kbd,
		peripherals: pm,
		nextEsxdos:  esx,
		nextDivMMC:  pager,
		nextDAC:     dacBank,
		nextSprites: sprites,
	}, nil
}

// NextDAC returns the Spectrum Next DAC bank when the harness was
// constructed with ModelNext, nil otherwise. Tests use this to peek
// at channel state after exercising port writes through the CPU
// / ULA dispatch path.
func (h *Harness) NextDAC() *dac.Bank { return h.nextDAC }

// NextSprites returns the Spectrum Next hardware sprite engine when
// the harness was constructed with ModelNext, nil otherwise. Tests
// use this to peek at sprite attribute/pattern state after
// exercising the $303B/$5B/$57 port-write path.
func (h *Harness) NextSprites() *sprite.Engine { return h.nextSprites }

// MountSDCard installs a directory-backed SD card on the Spectrum
// Next harness. Subsequent esxDOS F_OPEN / F_READ / F_FSTAT calls
// from guest code resolve paths under the given directory.
//
// Also enables divMMC automap. On real hardware the NextZXOS
// bootrom enables automap (via NextReg $06 bit 4) once it has
// finished its own early init; tests that mount an SD card are
// implicitly past that point in the boot, and need automap on
// so a synthesised RST $08 at PC=$0008 trips the divMMC overlay,
// which is what gates the esxDOS hook (see
// pkg/next/esxdos.HookFunc).
//
// Returns an error if the harness wasn't constructed for ModelNext
// or if the directory isn't usable. Calling MountSDCard(""/nil
// dir) detaches any existing mount.
func (h *Harness) MountSDCard(dir string) error {
	if h.nextEsxdos == nil {
		return fmt.Errorf("testharness: MountSDCard requires ModelNext")
	}
	if dir == "" {
		h.nextEsxdos.SetMount(nil)
		return nil
	}
	mount, err := sdcard.NewHostDir(dir)
	if err != nil {
		return fmt.Errorf("testharness: %w", err)
	}
	h.nextEsxdos.SetMount(mount)
	if h.nextDivMMC != nil {
		h.nextDivMMC.SetAutomap(true)
		// Open NR$B9 validation so RST $08 (esxDOS API entry) fires
		// automap. Default B9 = $01 only validates RST $00 (= cold
		// start); NextZXOS normally writes B9 itself after boot.
		// For SD-card unit tests we shortcut to the post-boot state.
		h.nextDivMMC.SetEntryPointsValid0(0xFF)
		// Also select the INSTANT automap variant (NR$BA bits set).
		// The default $00 = delayed_on, faithful to the FPGA: the
		// overlay pages in on the M1 AFTER the trigger fetch. The
		// esxDOS host-FS shim checks IsPagedIn() at the $0008 fetch
		// itself, so a delayed page-in would skip the dispatch and
		// the synthesised RST 8 would fall through unhandled.
		h.nextDivMMC.SetEntryPointsTiming0(0xFF)
	}
	return nil
}

// LoadNEX parses a .NEX file and loads its banks into Spectrum
// Next memory, then prepares the CPU to run from the .NEX entry
// point (SP, PC, border set from the header).
//
// Banks 0..127 are all supported. Bank 0..7 covers the
// classic 128K RAM range, and banks 8..127 use the Next's 2 MB
// memory map (allocated by memory.New when model == ModelNext).
// The entry bank is paged at 0xC000 via classic 7FFD when the
// entry bank is 0..7; entry banks ≥8 would require the 8K MMU
// (NextReg 0x50..0x57) since classic paging only addresses 8 banks.
// EntryBank>=8 is currently logged as a caveat — most .NEX files
// use 0..7 for the entry bank even when their bulk payload uses 8+.
func (h *Harness) LoadNEX(path string) error {
	if h.nextEsxdos == nil {
		return fmt.Errorf("testharness: LoadNEX requires ModelNext")
	}
	n, err := nex.ParseFile(path)
	if err != nil {
		return fmt.Errorf("testharness: %w", err)
	}
	for bank, data := range n.Banks {
		page := h.mem.GetPage(bank)
		if page == nil {
			log.Printf("testharness: LoadNEX(%s): bank %d not allocated — skipping", path, bank)
			continue
		}
		copy(page, data)
	}
	h.cpu.SP = n.Header.SP
	h.cpu.PC = n.Header.PC
	if n.Header.EntryBank >= 8 {
		log.Printf("testharness: LoadNEX(%s): entry bank %d > 7 — classic 7FFD paging can't map this. The .NEX must page itself via NextReg 0x50..0x57 before reaching its entry point.",
			path, n.Header.EntryBank)
	}
	h.mem.PageMemory(n.Header.EntryBank & 0x07)
	return nil
}
