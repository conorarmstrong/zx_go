package sdcard

// SD card SPI-mode emulator for divMMC.
//
// The Spectrum Next's divMMC bridges to an SD card over SPI:
//
// port 0xE7 (chip select) : bit 0 = card 0 CS, bit 1 = card 1 CS.
// Both active-low. Writing 0xFE selects
// card 0; 0xFF deselects both.
// port 0xEB (data) : write clocks 8 bits out; read clocks
// 8 bits in.
//
// SD SPI protocol (the subset divMMC + NextZXOS actually uses):
//
// * 6-byte command frames: 0x40|cmd, arg[31:24], arg[23:16],
// arg[15:8], arg[7:0], CRC. Top two bits of byte 0 are "01" so
// start of frame is detectable as (byte & 0xC0) == 0x40.
// * After a command, host polls IN(0xEB) for the response. While
// the card is computing, it returns 0xFF.
// * R1 response: 1 byte. Bit 0 = idle, bit 1 = erase reset, etc.
// * R3/R7 response: R1 followed by 4 extra bytes.
// * Data read: R1, then 0xFE token, then N data bytes, then 2 CRC.
//
// We implement the commands divMMC and NextZXOS issue at boot:
//
// CMD0 GO_IDLE_STATE -> R1=0x01
// CMD8 SEND_IF_COND -> R7 (echoes voltage + check pattern)
// CMD9 SEND_CSD -> R1=0x00 + 16-byte CSD
// CMD10 SEND_CID -> R1=0x00 + 16-byte CID
// CMD12 STOP_TRANSMISSION -> R1=0x00
// CMD16 SET_BLOCKLEN -> R1=0x00
// CMD17 READ_SINGLE_BLOCK -> R1=0x00 + 0xFE + 512 + CRC16
// CMD24 WRITE_BLOCK -> R1=0x00; host then sends 0xFE + 512
// CMD55 APP_CMD -> R1=0x01 (sets ACMD prefix)
// CMD58 READ_OCR -> R3 (OCR with CCS=1 for SDHC)
// ACMD41 SD_SEND_OP_COND -> R1=0x01 then 0x00 (initialization)
//
// Block addressing is SDHC-style: CMD17 arg is the LBA (not byte
// offset). This is what NextZXOS expects.

import (
	"sync"
)

// BlockSource is what backs a virtual SD card. Cards are
// 512-byte-block-addressed; LBA 0 is the MBR / boot sector.
type BlockSource interface {
	// ReadBlock copies 512 bytes for the given LBA into dst.
	// dst is guaranteed to be exactly 512 bytes.
	ReadBlock(lba uint32, dst []byte) error
	// WriteBlock copies 512 bytes from src into the LBA. Optional;
	// returning nil from a read-only source rejects writes.
	WriteBlock(lba uint32, src []byte) error
	// Capacity is the number of 512-byte blocks. Used for the
	// CSD response so the host knows the card size.
	Capacity() uint32
}

// Card is a single SD card on the divMMC SPI bus. It models the
// state of one card and decodes SPI traffic into commands.
//
// Thread-safety: divMMC port writes run on the CPU goroutine, but
// some tests poke the card concurrently. The mutex guards the
// state machine. The hot path (port write / read) takes the lock
// only once per byte.
type Card struct {
	mu sync.Mutex

	src BlockSource

	// SPI command framing.
	cmdBuf [6]byte
	cmdLen int

	// Bytes the host will read next via IN(0xEB). Used for R1/R3/R7
	// responses, data tokens, and block payloads. When empty,
	// IN(0xEB) returns 0xFF (the SPI idle byte).
	rxQueue []byte

	// Write-block state. When the host sends a data byte after
	// CMD24, we buffer until we have the data token + 512 + 2 CRC
	// then commit via WriteBlock.
	writeMode bool
	writeBuf  []byte

	// Multi-block write state (CMD25 WRITE_MULTIPLE_BLOCK). After
	// CMD25 the host streams blocks, each framed by a 0xFC
	// "multi-write start" token + 512 data + 2 CRC, until it sends a
	// 0xFD "stop tran" token. Each accepted block writes to the next
	// sequential LBA. multiWriteCollecting is true while accumulating
	// a block's payload (between the 0xFC token and its 514th byte).
	multiWriteMode       bool
	multiWriteCollecting bool
	multiWriteLBA        uint32

	// Erase state (CMD32 ERASE_WR_BLK_START / CMD33 ERASE_WR_BLK_END
	// / CMD38 ERASE). CMD32/33 latch the inclusive block range; CMD38
	// zeroes it. eraseStartSet guards CMD38 against running with no
	// prior range set.
	eraseStart    uint32
	eraseEnd      uint32
	eraseStartSet bool

	// Multi-block read state (CMD18 READ_MULTIPLE_BLOCK). When
	// multiReadActive is true, the card auto-queues the next 512-
	// byte block whenever the host has drained the current block,
	// continuing until CMD12 (STOP_TRANSMISSION) arrives. Per the
	// SD Physical Layer Spec, multi-read addresses are SDHC-style
	// (LBA) or SDSC-style (byte address) depending on the OCR CCS
	// bit — same rule as CMD17.
	multiReadActive bool
	multiReadLBA    uint32

	// CMD55 sets this; consumed by the next command, which is then
	// interpreted as an ACMDxx.
	appCmd bool

	// inIdle tracks the SD-spec "card in idle state" flag that R1
	// bit 0 reports. True from power-up / CMD0; cleared when
	// ACMD41 returns ready ($00). Every command that returns R1
	// must encode this bit correctly — hardcoding R1=$01 (the
	// pre-iter-133 behaviour) caused the bank-2 FAT16 driver to
	// see "card still idle" on CMD55 calls long after init had
	// completed, which mismatched the reference and may have
	// confused the post-init code paths.
	inIdle bool

	// initOpCondPending is the number of ACMD41 polls the host must
	// make before we return "ready" (R1=0x00). Real cards take a
	// few ms; we make it a small count so tests are fast but the
	// host still sees the standard "polling" behaviour.
	acmd41Remaining int

	// csSelected is true while CS is asserted (low). The host
	// rotates CS between commands; we use this to detect frame
	// boundaries for some edge cases.
	csSelected bool

	// logger, if non-nil, is called with each dispatched SPI
	// command for instrumentation. (cmd, arg, isACMD).
	logger func(cmd byte, arg uint32, isACMD bool)

	// byteLogger, if non-nil, is called with each byte clocked
	// (write=true if host->card, false if card->host). Useful for
	// debugging SPI framing issues against real-hardware traces.
	byteLogger func(write bool, val byte)

	// advertiseSDHC selects whether CMD58's OCR response sets CCS
	// (Card Capacity Status, bit 30 of the 32-bit OCR). When true
	// we advertise SDHC/SDXC (block-addressed); when false SDSC
	// (byte-addressed). Default false to match the legacy
	// NextZXOS bank-2 SD driver path; set true to investigate the
	// FPGA-bootrom path which expects SDHC addressing.
	advertiseSDHC bool
}

// SetSDHC toggles the OCR's CCS bit so this card advertises
// SDHC/SDXC capacity (block-addressed CMD17 args) instead of the
// default SDSC (byte-addressed).
func (c *Card) SetSDHC(on bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.advertiseSDHC = on
}

// SetLogger installs a per-command callback for debugging. Pass
// nil to disable. The callback receives the 6-bit command index,
// the 32-bit argument, and whether the command was preceded by a
// CMD55 (so isACMD=true means this is an ACMDxx).
func (c *Card) SetLogger(fn func(cmd byte, arg uint32, isACMD bool)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.logger = fn
}

// SetByteLogger installs a per-byte callback fired on every SPI
// data byte clocked through the card. write=true means host -> card
// (WriteData), write=false means card -> host (ReadData). Pass nil
// to disable. Intended for SPI-framing-level debugging only — busy
// hot path.
func (c *Card) SetByteLogger(fn func(write bool, val byte)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.byteLogger = fn
}

// NewCard returns a fresh card backed by src. A nil src means the
// slot is empty — IN(0xEB) returns 0xFF unconditionally and all
// commands stay in idle.
func NewCard(src BlockSource) *Card {
	// Cards power up in idle state; the host issues CMD0 → CMD8 →
	// CMD55+ACMD41 to leave idle. ACMD41 clears `inIdle` when it
	// responds ready ($00).
	//
	// acmd41Remaining=0 → respond $00 (= ready) on the first ACMD41
	// poll, matching the reference's SPI emulation. Real SD cards may take
	// several polls before settling on $00 (cards need ~1 s to power
	// up internally); the reference skips the delay because most software
	// busy-waits on ACMD41 then assumes the card is ready.
	return &Card{src: src, inIdle: true, acmd41Remaining: 0}
}

// r1IdleBit returns the R1 byte for "normal" responses — bit 0 set
// while the card is in idle state, clear once ACMD41 has completed.
// Use this for every R1-returning command instead of hardcoding $01.
func (c *Card) r1IdleBit() byte {
	if c.inIdle {
		return 0x01
	}
	return 0x00
}

// HasMedia reports whether a backing block source is present.
func (c *Card) HasMedia() bool { return c != nil && c.src != nil }

// WriteCS handles a write to port 0xE7. Per the divMMC spec, bit 0
// is the CS for SD slot 0 (master) and bit 1 is the CS for SD slot
// 1 (slave). Both are active-low.
//
// Real Spectrum Next has TWO SD slots — most users have one card
// in either slot. NextZXOS's bank-2 SD driver probes BOTH slots by
// alternating between $FE (slot 0 asserted) and $FD (slot 1
// asserted) when looking for a responding card.
//
// We model a single card responding to EITHER slot — if either CS
// bit is clear, treat our card as selected. This matches what real
// hardware with a card in either slot does for the OS's probe.
//
// On every CS edge we drop any unread response bytes and reset
// the command-frame buffer.
func (c *Card) WriteCS(val byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	// Port $E7 decodes per-slot chip selects: bit 0 = SD slot 0 CS_n,
	// bit 1 = SD slot 1 CS_n (active low). This Card models the card in
	// SLOT 0 only — it must NOT respond to slot-1 selects. The earlier
	// either-bit decode made our single card answer the slot-1 probe too:
	// NextZXOS's per-slot init ($09AF probes slot 0 then slot 1) detected
	// a PHANTOM second card (B=2), registered two drive slots, and looped
	// forever trying to mount the phantom — the $3BF5 deliberate-reset
	// loop (the development log). The reference/hardware with one card return B=1.
	asserted := val&0x01 == 0
	if c.csSelected != asserted {
		c.cmdLen = 0
		c.rxQueue = c.rxQueue[:0]
		c.writeMode = false
		c.writeBuf = c.writeBuf[:0]
		// multiReadActive deliberately SURVIVES a CS toggle: an SPI
		// SD card's multi-block read state machine is only ended by
		// CMD12 (or a new command), not by deselecting the card.
		// NextZXOS's DOS keeps one CMD18 stream open across driver
		// calls, releasing CS between chunks; resetting here starved
		// every chunk after the first and broke all multi-block
		// module loads (the development log). Pending queue bytes are
		// still dropped — drivers only release CS on block
		// boundaries, and the auto-queue resumes at the next block.
		c.multiWriteMode = false
		c.multiWriteCollecting = false
	}
	c.csSelected = asserted
}

// WriteData handles a byte clocked out by the host (OUT 0xEB,A).
// On a card with no media this is a no-op — the host's clocks
// are absorbed but produce no response.
func (c *Card) WriteData(val byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.byteLogger != nil {
		c.byteLogger(true, val)
	}
	// CS deasserted (or a different slot selected): a real SPI card
	// ignores MOSI clocks entirely. Without this gate our slot-0 card
	// processed command frames addressed to slot 1 (the development log).
	if !c.csSelected {
		return
	}
	if c.src == nil {
		return
	}
	if c.writeMode {
		if c.multiWriteMode {
			c.handleMultiWriteByte(val)
		} else {
			c.handleWriteByte(val)
		}
		return
	}
	c.handleCommandByte(val)
}

// ReadData handles a byte clocked in by the host (IN A,(0xEB)).
// Returns 0xFF when no card is present or no response is queued.
func (c *Card) ReadData() byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	// CS deasserted: MISO floats high — the host reads $FF (no card).
	if !c.csSelected {
		if c.byteLogger != nil {
			c.byteLogger(false, 0xFF)
		}
		return 0xFF
	}
	// If a multi-block read is in progress and the host has drained
	// the current block's response (R1 + token + 512 + CRC), queue
	// the next block automatically. Real SD cards stream data
	// continuously between CMD18 and CMD12 — they don't expect the
	// host to re-issue CMD17 per block.
	if c.multiReadActive && len(c.rxQueue) == 0 && c.src != nil {
		buf := make([]byte, 512)
		_ = c.src.ReadBlock(c.multiReadLBA, buf)
		// Each streamed block is framed by the data token + 512
		// payload + 2 CRC bytes, exactly like CMD17's response
		// body (minus the leading R1, which only precedes the
		// first block). A real card needs access time between
		// blocks (SD spec Nac ≥ 1 byte) and holds the line idle
		// ($FF) until the next block is ready. The NextZXOS bank-2
		// block reader (ROM2 $1950) blindly clocks 2 extra bytes
		// after each block's CRC; with zero gap those reads swallow
		// the next token + first data byte and every later block
		// arrives shifted by 2 — corrupting all multi-block module
		// loads (the development log). Two idle bytes absorb the blind
		// reads; token-polling readers skip them as idle.
		c.rxQueue = append(c.rxQueue, 0xFF, 0xFF)
		c.queueRawDataBlock(buf)
		c.multiReadLBA++
	}
	var b byte = 0xFF
	if len(c.rxQueue) > 0 {
		b = c.rxQueue[0]
		c.rxQueue = c.rxQueue[1:]
	}
	if c.byteLogger != nil {
		c.byteLogger(false, b)
	}
	return b
}

// handleCommandByte assembles a 6-byte SPI command frame and
// dispatches once full. Skips leading 0xFF idle bytes — the host
// often sends a few 0xFFs before the real command.
func (c *Card) handleCommandByte(val byte) {
	if c.cmdLen == 0 {
		// Wait for a "start of frame" byte: top two bits 01.
		if val&0xC0 != 0x40 {
			return
		}
	}
	c.cmdBuf[c.cmdLen] = val
	c.cmdLen++
	if c.cmdLen < 6 {
		return
	}
	c.cmdLen = 0
	c.dispatch()
}

// dispatch executes the command currently in cmdBuf.
func (c *Card) dispatch() {
	cmd := c.cmdBuf[0] & 0x3F // strip start bits
	arg := uint32(c.cmdBuf[1])<<24 |
		uint32(c.cmdBuf[2])<<16 |
		uint32(c.cmdBuf[3])<<8 |
		uint32(c.cmdBuf[4])

	if c.logger != nil {
		c.logger(cmd, arg, c.appCmd)
	}

	if c.appCmd {
		c.appCmd = false
		c.dispatchACMD(cmd, arg)
		return
	}

	switch cmd {
	case 0: // GO_IDLE_STATE
		// CMD0 always puts the card BACK into idle, regardless of
		// previous state. Re-arm ACMD41 init handshake too.
		c.inIdle = true
		c.acmd41Remaining = 0
		c.respondR1(0x01)
	case 1: // SEND_OP_COND (MMC; we just respond ready)
		c.respondR1(0x00)
	case 8: // SEND_IF_COND
		// R7 = R1 + 4 echo bytes. R1 reflects idle state per SD spec.
		r := []byte{0xFF, 0xFF /* Ncr=2, matches respondR1 */, c.r1IdleBit(), c.cmdBuf[1], c.cmdBuf[2], c.cmdBuf[3], c.cmdBuf[4]}
		c.queue(r)
	case 9: // SEND_CSD: 0xFF Ncr + R1 + 0xFE token + 16 CSD + 2x 0xFF CRC.
		// Matches the canonical MMC SPI lines 882-919 byte-for-byte.
		c.respondR1(0x00) // respondR1 supplies the Ncr pad
		c.queueRawDataBlock(c.csdBytes())
	case 10: // SEND_CID: same layout as CMD9.
		c.respondR1(0x00) // respondR1 supplies the Ncr pad
		c.queueRawDataBlock(c.cidBytes())
	case 12: // STOP_TRANSMISSION
		// Terminate any in-progress multi-read stream. A real card stops
		// driving block data the moment it accepts CMD12 — the unread
		// remainder of the current block (and any not-yet-streamed next
		// block) is ABORTED, never sent; the next bytes on MISO are the
		// R1 then idle. Leaving the remainder queued fed the host stale
		// data bytes as the CMD12 response: TBBLUE.FW reads them as
		// command responses, desyncs, concludes the card is wedged, and
		// re-initialises forever — the 398× CMD0 cold-boot loop. The
		// reference emulator aborts here and boots (the development log).
		// Real cards send R1b (R1 + busy bytes); the busy is satisfied
		// by the host's clock idle so we just queue R1=0.
		c.multiReadActive = false
		c.rxQueue = c.rxQueue[:0]
		c.respondR1(0x00)
	case 13: // SEND_STATUS — R2 (2 bytes)
		c.queue([]byte{0xFF /* Ncr */, 0x00, 0x00})
	case 16: // SET_BLOCKLEN
		c.respondR1(0x00)
	case 17: // READ_SINGLE_BLOCK
		c.handleReadBlock(arg)
	case 18: // READ_MULTIPLE_BLOCK
		// Start a streaming read: queue the first block now, then
		// auto-queue subsequent blocks as the host drains each one
		// until it issues CMD12 (or a CS deassert).
		c.handleReadBlock(arg)
		// Track where we are so ReadData can fetch the next block.
		// arg is byte address (SDSC) or LBA (SDHC) — convert to LBA.
		if c.advertiseSDHC {
			c.multiReadLBA = arg + 1
		} else {
			c.multiReadLBA = (arg / 512) + 1
		}
		c.multiReadActive = true
	case 24: // WRITE_BLOCK
		c.handleWriteBlockStart(arg)
	case 25: // WRITE_MULTIPLE_BLOCK
		c.handleWriteMultiBlockStart(arg)
	case 32: // ERASE_WR_BLK_START
		c.eraseStart = arg
		c.eraseStartSet = true
		c.respondR1(0x00)
	case 33: // ERASE_WR_BLK_END
		c.eraseEnd = arg
		c.respondR1(0x00)
	case 38: // ERASE
		c.handleErase()
	case 55: // APP_CMD prefix
		// R1 reflects idle state per SD spec — bit 0 set while
		// card is in idle, clear once ACMD41 has completed.
		c.appCmd = true
		c.respondR1(c.r1IdleBit())
	case 58: // READ_OCR
		// R3 = R1 + 4-byte OCR. Byte 0 has bit 7 = power-up done,
		// bit 6 = CCS (Card Capacity Status: 0=SDSC byte-addressed,
		// 1=SDHC/SDXC block-addressed). Bytes 1-3 advertise the
		// full 2.7-3.6V voltage range.
		//
		// Historical: an earlier comment said CCS=1 breaks NextZXOS
		// (after the FPGA bootrom has done the personality switch).
		// In the FPGA-bootrom (path B) phase the bootrom expects
		// the addressing mode it negotiated, and ZX_GO_NEXT_SDHC=1
		// flips us to SDHC to investigate that path. Default is
		// SDSC to match the existing NextZXOS bank-2 driver path.
		ocr0 := byte(0x80) // power-up done
		if c.advertiseSDHC {
			ocr0 |= 0x40 // CCS=1 — SDHC/SDXC
		}
		// OCR voltage-window bytes are 0x00. The FPGA core bootrom reads the
		// 4 OCR bytes via its skip-0xFF response reader ($05AD:
		// `in a,($EB); cp $FF; ret nz`), so any 0xFF in the OCR is silently
		// swallowed and the bootrom reads one byte short, times out, and
		// wrongly takes the TBBLUE.FW core-load branch instead of NextZXOS.
		// The reference emulator returns OCR = ocr0,$00,$00,$00 here (verified
		// by instruction-lockstep, D18e); matching it lets the bootrom read
		// all 4 bytes. The generic SD spec sets voltage bits ($FF $80) but the
		// Next core cannot tolerate that — this is the Next's actual behaviour.
		c.queue([]byte{0xFF, 0xFF /* Ncr=2, matches respondR1 */, 0x00, ocr0, 0x00, 0x00, 0x00})
	case 59: // CRC_ON_OFF
		c.respondR1(0x00)
	default:
		c.respondR1(0x04) // illegal command
	}
}

func (c *Card) dispatchACMD(cmd byte, _ uint32) {
	switch cmd {
	case 41: // SD_SEND_OP_COND
		if c.acmd41Remaining > 0 {
			c.acmd41Remaining--
			c.respondR1(0x01) // still initializing — card stays in idle
		} else {
			// Ready: clear the idle flag so subsequent CMD55 / CMD8
			// / etc. return R1 with bit 0 = 0 per SD spec.
			c.inIdle = false
			c.respondR1(0x00)
		}
	case 13: // SD_STATUS
		c.respondR1(0x00)
	case 51: // SEND_SCR
		c.respondR1(0x00)
		// 8-byte SCR: SD spec v2, no security, 1-bit bus only.
		c.queueDataBlock([]byte{0x02, 0x35, 0x83, 0x00, 0x00, 0x00, 0x00, 0x00}, 8)
	default:
		c.respondR1(0x04)
	}
}

// queue appends bytes to the response queue.
func (c *Card) queue(b []byte) {
	c.rxQueue = append(c.rxQueue, b...)
}

// crc16CCITT computes the SD data-block CRC: CRC-16/CCITT with
// polynomial 0x1021 and initial value 0x0000 (the "XMODEM" variant the
// SD Physical Layer spec mandates for SPI-mode data blocks). Real cards
// — and the reference SD model — return this over each block's
// payload, high byte first. We formerly sent dummy $FF/$00 CRC bytes;
// esxDOS reads the trailing CRC bytes and the divergence vs the
// reference was the Guide crash's first divergent read.
func crc16CCITT(data []byte) uint16 {
	var crc uint16
	for _, b := range data {
		crc ^= uint16(b) << 8
		for i := 0; i < 8; i++ {
			if crc&0x8000 != 0 {
				crc = crc<<1 ^ 0x1021
			} else {
				crc <<= 1
			}
		}
	}
	return crc
}

// queueBusy queues n 0xFF bytes — used to model the variable
// queueRawDataBlock queues an SDSC-style data block: 0xFE token
// + exactly len(data) bytes + 2x 0xFF CRC bytes. Used for CMD9
// (CSD) and CMD10 (CID), each of which returns exactly 16 bytes.
// Matches the canonical MMC SPI CRC bytes (0xFF 0xFF, not 0x00 0x00).
func (c *Card) queueRawDataBlock(data []byte) {
	// One $FF Nac (access-time) filler precedes the data token —
	// matches the reference model (the development log; real cards
	// have Nac >= 1 byte between R1 and the token).
	c.rxQueue = append(c.rxQueue, 0xFF, 0xFE)
	c.rxQueue = append(c.rxQueue, data...)
	// Real CRC-16/CCITT over the payload, high byte first — what real
	// cards and the reference send. (Was dummy $FF $FF, which esxDOS
	// reads and diverged on — the Guide crash.)
	crc := crc16CCITT(data)
	c.rxQueue = append(c.rxQueue, byte(crc>>8), byte(crc))
}

// respondR1 queues a 1-byte R1 response, preceded by one $FF Ncr
// byte. A real card holds DataOut high for at least one byte between
// a command's last bit and the response (SD spec Ncr = 1..8 bytes);
// answering immediately made fixed-count SPI readers in TBBLUE.FW see
// the response one byte early — the first divergent instruction vs
// the reference emulator ($78E5 fork, the development log). CMD9/10/17/18 already
// padded via queueBusy(1) (now removed there); this makes every
// response uniform with the canonical MMC SPI model.
func (c *Card) respondR1(r1 byte) {
	// Ncr = 2 filler bytes before R1: matches the reference SD model
	// (real-card spec allows Ncr=1-8; the dense-trace diff vs the
	// reference showed exactly two — the development log/D31du).
	c.rxQueue = append(c.rxQueue, 0xFF, 0xFF, r1)
}

// queueDataBlock queues a data token (0xFE), the data padded to
// length n, then 2 CRC bytes (we use 0x00 0x00 since CRC checks
// are typically disabled in SPI mode).
func (c *Card) queueDataBlock(data []byte, n int) {
	// Build the exact n-byte payload (truncate or zero-pad), so the
	// CRC covers precisely the bytes sent.
	payload := make([]byte, n)
	copy(payload, data)
	// Nac filler before the token — see queueRawDataBlock.
	c.rxQueue = append(c.rxQueue, 0xFF, 0xFE)
	c.rxQueue = append(c.rxQueue, payload...)
	// Real CRC-16/CCITT over the payload, high byte first (was $00 $00).
	crc := crc16CCITT(payload)
	c.rxQueue = append(c.rxQueue, byte(crc>>8), byte(crc))
}

// handleReadBlock dispatches CMD17/18 with the same response
// framing the CMD9 / CMD10 paths use, byte-for-byte matching
// the canonical MMC SPI implementation:
//
// one 0xFF Ncr pad → R1 (0x00) → 0xFE data token → 512 bytes
// of payload → 0xFF 0xFF CRC.
//
// SD ADDRESSING: SDSC cards (which we advertise via OCR CCS=0)
// expect the CMD17 argument to be a BYTE address — the host
// multiplies the desired sector by 512 before issuing the
// command. Our `src.ReadBlock` API takes LBA, so we divide back
// out. SDHC would skip that step (the arg is the LBA directly),
// but we never advertise SDHC: the canonical MMC SPI implementation comment
// records that CCS=1 breaks NextZXOS, and we matched that
// decision in our OCR.
func (c *Card) handleReadBlock(arg uint32) {
	c.respondR1(0x00) // respondR1 supplies the Ncr pad
	buf := make([]byte, 512)
	if c.src != nil {
		sector := arg
		if !c.advertiseSDHC {
			// SDSC: arg is byte address — divide back to LBA.
			sector = arg / 512
		}
		// SDHC/SDXC: arg is already the LBA (block number).
		_ = c.src.ReadBlock(sector, buf)
	}
	c.queueRawDataBlock(buf)
}

// handleWriteBlockStart begins a write transaction. The host
// will send a 0xFE data token followed by 512 bytes + 2 CRC.
func (c *Card) handleWriteBlockStart(lba uint32) {
	c.respondR1(0x00)
	c.writeMode = true
	c.writeBuf = make([]byte, 0, 512+3)
	c.writeBuf = append(c.writeBuf, byte(lba>>24), byte(lba>>16), byte(lba>>8), byte(lba))
}

// handleWriteByte accumulates bytes during a CMD24 write phase.
// State: first we wait for the 0xFE data token, then collect 512
// data bytes + 2 CRC bytes, then respond with a data-accepted
// token (0x05) and a "not busy" 0xFF byte.
func (c *Card) handleWriteByte(val byte) {
	// First 4 bytes of writeBuf hold the LBA we set up at start.
	// After that, byte 4 is the token slot; we look for 0xFE.
	dataStart := 4
	if len(c.writeBuf) == dataStart {
		// Waiting for data token.
		if val == 0xFE {
			c.writeBuf = append(c.writeBuf, val)
		} else if val != 0xFF {
			// Some other unexpected byte — abort.
			c.writeMode = false
		}
		return
	}
	c.writeBuf = append(c.writeBuf, val)
	// 4 LBA + 1 token + 512 data + 2 CRC = 519 bytes total.
	if len(c.writeBuf) >= 4+1+512+2 {
		lba := uint32(c.writeBuf[0])<<24 |
			uint32(c.writeBuf[1])<<16 |
			uint32(c.writeBuf[2])<<8 |
			uint32(c.writeBuf[3])
		data := c.writeBuf[5 : 5+512]
		if c.src != nil {
			_ = c.src.WriteBlock(lba, data)
		}
		c.writeMode = false
		c.writeBuf = nil
		// Data-accepted token = 0x05 (lower 5 bits = 00010 wrapped).
		c.rxQueue = append(c.rxQueue, 0x05, 0xFF)
	}
}

// handleWriteMultiBlockStart begins a CMD25 multi-block write. The
// host then streams, per block, a 0xFC start token + 512 data + 2
// CRC; each accepted block writes to the next sequential LBA. A
// 0xFD stop-tran token ends the transaction. Addressing matches the
// CMD24 sibling: arg is the start LBA.
func (c *Card) handleWriteMultiBlockStart(lba uint32) {
	c.respondR1(0x00)
	c.writeMode = true
	c.multiWriteMode = true
	c.multiWriteCollecting = false
	c.multiWriteLBA = lba
	c.writeBuf = c.writeBuf[:0]
}

// handleMultiWriteByte drives the CMD25 streaming state machine.
// Between blocks it waits for a 0xFC (start next block) or 0xFD
// (stop transmission) token, ignoring 0xFF idle bytes. While
// collecting it accumulates 512 data + 2 CRC, commits the block to
// the next LBA, and emits a data-accepted token (0x05) + not-busy
// (0xFF).
func (c *Card) handleMultiWriteByte(val byte) {
	if !c.multiWriteCollecting {
		switch val {
		case 0xFC: // multi-write data block start token
			c.multiWriteCollecting = true
			c.writeBuf = c.writeBuf[:0]
		case 0xFD: // stop-tran token — end the multi-write
			c.multiWriteMode = false
			c.writeMode = false
			c.writeBuf = c.writeBuf[:0]
			c.rxQueue = append(c.rxQueue, 0xFF) // card not busy
		case 0xFF:
			// Idle byte between blocks — ignore.
		default:
			// Unexpected byte — abort the transaction defensively.
			c.multiWriteMode = false
			c.writeMode = false
		}
		return
	}
	c.writeBuf = append(c.writeBuf, val)
	if len(c.writeBuf) >= 512+2 { // 512 data + 2 CRC
		if c.src != nil {
			_ = c.src.WriteBlock(c.multiWriteLBA, c.writeBuf[:512])
		}
		c.multiWriteLBA++
		c.multiWriteCollecting = false
		c.writeBuf = c.writeBuf[:0]
		// Data-accepted token (0x05) + one not-busy byte.
		c.rxQueue = append(c.rxQueue, 0x05, 0xFF)
	}
}

// handleErase zeroes the inclusive block range latched by CMD32
// (start) and CMD33 (end). Real SD cards erase to the card's
// DATA_STAT_AFTER_ERASE value (0 or 1); we model the common 0x00
// fill. Responds R1=0 (the R1b busy is satisfied by the host's
// idle clocks, like CMD12). A CMD38 with no prior CMD32 returns an
// error so a stray erase can't wipe block 0.
func (c *Card) handleErase() {
	if !c.eraseStartSet {
		c.respondR1(0x04) // illegal command — no range set
		return
	}
	start, end := c.eraseStart, c.eraseEnd
	if end < start {
		start, end = end, start
	}
	if c.src != nil {
		zero := make([]byte, 512)
		for lba := start; lba <= end; lba++ {
			_ = c.src.WriteBlock(lba, zero)
			if lba == end { // guard against uint32 wrap on end==MaxUint32
				break
			}
		}
	}
	c.eraseStartSet = false
	c.respondR1(0x00)
}

// csdBytes returns a 16-byte CSD v1 (SDSC). Byte values match
// the canonical MMC SPI implementation mmc_insert exactly: every byte is
// the `0x0B` initialiser fill except bytes 5, 6, 7, 8, 9, A which
// are derived from capacity. NextZXOS's bank-2 driver is only
// validated against SDSC; CSD v2 / SDHC sends the driver into a
// re-init loop.
func (c *Card) csdBytes() []byte {
	if c.advertiseSDHC {
		return c.csdBytesV2()
	}
	csd := make([]byte, 16)
	for i := range csd {
		csd[i] = 0x0B
	}
	// Capacity in bytes. Cap to ~2GB (SDSC limit).
	const maxSDSCBytes = uint64(2) * 1024 * 1024 * 1024
	capBytes := uint64(0)
	if c.src != nil {
		capBytes = uint64(c.src.Capacity()) * 512
	}
	if capBytes > maxSDSCBytes {
		capBytes = maxSDSCBytes
	}
	// MMC card layout: for <1 GiB cards, sector_size=512 (8-bit=9),
	// cmult=512 (8-bit=7). multiple = 512*512 = 262144.
	// device_size = (capBytes / multiple) << 6
	// byte 5 = sector_size_8_bits
	// byte 6 = (device_size >> 16) & 3
	// byte 7 = (device_size >> 8) & 255
	// byte 8 = device_size & 0xC0
	// byte 9 = (cmult_8_bits >> 1) & 3
	// byte A = (cmult_8_bits << 7) & 128
	const sectorSize8 = byte(9)
	const cmult8 = byte(7)
	const multiple = uint64(512 * 512)
	deviceSize := (capBytes / multiple) << 6
	csd[5] = sectorSize8
	csd[6] = byte((deviceSize >> 16) & 3)
	csd[7] = byte((deviceSize >> 8) & 0xFF)
	csd[8] = byte(deviceSize & 0xC0)
	csd[9] = byte((cmult8 >> 1) & 3)
	csd[10] = byte(uint16(cmult8) << 7 & 0x80)
	return csd
}

// csdBytesV2 returns a 16-byte CSD version 2.0, as a real SDHC/SDXC card
// returns. A high-capacity card (OCR CCS=1, advertised via SetSDHC) MUST
// pair that with a v2 CSD: the NextZXOS SD driver reads CMD9 right after
// CMD58 and rejects the card (re-init loop) if the CSD structure version
// contradicts the OCR's CCS bit. The constant fields below come from the
// SD Physical Layer spec's CSD v2.0 example; capacity is carried in the
// 22-bit C_SIZE field where memory = (C_SIZE + 1) × 512 KiB.
//
// Bit layout (CSD[127:0], MSB first as csd[0..15]):
//
//	[127:126] CSD_STRUCTURE = 01b           -> csd[0] = 0x40
//	[119:112] TAAC = 0x0E, [111:104] NSAC=0 -> csd[1]=0x0E, csd[2]=0x00
//	[103:96]  TRAN_SPEED = 0x32 (25 MHz)    -> csd[3] = 0x32
//	[95:84]   CCC = 0x5B5, [83:80] READ_BL_LEN=9 -> csd[4]=0x5B, csd[5]=0x59
//	[69:48]   C_SIZE (22 bits)              -> csd[7..9]
//	[46]      ERASE_BLK_EN=1, [45:39] SECTOR_SIZE=0x7F -> csd[10]=0x7F, csd[11]=0x80
//	[28:26]   R2W_FACTOR=010, [25:22] WRITE_BL_LEN=9   -> csd[12]=0x0A, csd[13]=0x40
//	[0]       always 1 (+ CRC7, ignored in SPI)        -> csd[15]=0x01
func (c *Card) csdBytesV2() []byte {
	capBytes := uint64(0)
	if c.src != nil {
		capBytes = uint64(c.src.Capacity()) * 512
	}
	// C_SIZE = capacity / 512 KiB - 1, clamped to the 22-bit field. A
	// card smaller than one 512 KiB unit reports the minimum (C_SIZE=0).
	const unit = uint64(512 * 1024)
	var cSize uint32
	if capBytes >= unit {
		cSize = uint32(capBytes/unit - 1)
	}
	if cSize > 0x3FFFFF {
		cSize = 0x3FFFFF
	}
	csd := make([]byte, 16)
	csd[0] = 0x40 // CSD_STRUCTURE = 01b (v2.0)
	csd[1] = 0x0E // TAAC
	csd[2] = 0x00 // NSAC
	csd[3] = 0x32 // TRAN_SPEED = 25 MHz
	csd[4] = 0x5B // CCC high
	csd[5] = 0x59 // CCC low (0x5) | READ_BL_LEN (9)
	csd[6] = 0x00 // READ_BL_PARTIAL=0 .. DSR_IMP=0, reserved
	csd[7] = byte((cSize >> 16) & 0x3F)
	csd[8] = byte((cSize >> 8) & 0xFF)
	csd[9] = byte(cSize & 0xFF)
	csd[10] = 0x7F // ERASE_BLK_EN=1 | SECTOR_SIZE[6:1]
	csd[11] = 0x80 // SECTOR_SIZE[0] | WP_GRP_SIZE=0
	csd[12] = 0x0A // R2W_FACTOR=2 | WRITE_BL_LEN[3:2]
	csd[13] = 0x40 // WRITE_BL_LEN[1:0] | reserved
	csd[14] = 0x00 // file-format group etc.
	csd[15] = 0x01 // CRC7 (ignored in SPI) + always-1 bit
	return csd
}

// cidBytes returns a 16-byte CID. Mostly zeroes; the host doesn't
// inspect it during boot.
func (c *Card) cidBytes() []byte {
	cid := make([]byte, 16)
	cid[0] = 0x03                                              // mfr ID
	copy(cid[1:3], []byte{0x53, 0x44})                         // OEM "SD"
	copy(cid[3:8], []byte{'Z', 'X', '_', 'G', 'O'})            // product name
	cid[8] = 0x10                                              // revision
	cid[9], cid[10], cid[11], cid[12] = 0x00, 0x00, 0x00, 0x01 // serial
	cid[13] = 0x00                                             // reserved + mdt high
	cid[14] = 0x00                                             // mdt low
	cid[15] = 0x01                                             // CRC + always-1
	return cid
}
