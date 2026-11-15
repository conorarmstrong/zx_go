package next

// BusDelayer is the zxnDMA's dma_delay_i pin.
type BusDelayer interface{ SetBusDelay(on bool) }

// InstructionHook returns the CPU's per-instruction callback for the Next: it
// advances the CTC and the UARTs by the instruction's T-states and then samples
// dma_delay_i (see IM2Driver.DMADelay for why one sample per boundary is
// exact). dma is a function because the machine builds the DMA after this
// hook is installed; it may return nil.
func InstructionHook(speed func() int, ctc *CTCClock, uart *UARTSystem, im2 *IM2Driver, dma func() BusDelayer) func(tstates int) {
	return func(tstates int) {
		s := speed()
		ctc.Advance(tstates, s)
		uart.Advance(tstates, s)
		if dma == nil {
			return
		}
		if b := dma(); b != nil {
			b.SetBusDelay(im2.DMADelay())
		}
	}
}
