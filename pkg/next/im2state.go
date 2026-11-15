package next

import (
	"bytes"
	"encoding/gob"
	"fmt"
)

// Complete state capture for the IM2 driver and the daisy chain behind it, for
// rewind and save states.
//
// Everything is captured: the mode and vector base, the request, enable and
// status-clear pins, and per peripheral the delayed request, the sticky
// request latch, the sticky status record, the delayed isr-serviced level and
// the device FSM state, plus the chain's last combinational outputs. A restore
// does not call the /INT listener: the CPU's own latch is part of the CPU's
// captured state, and replaying a transition here would assert it twice.

type im2PeripheralState struct {
	IntReqD, Im2IntReq, IntStatus, IsrServicedD bool
	State                                       int
}

type im2State struct {
	HWIM2      bool
	VectorBase byte
	Req        [IM2NumPeriph]bool
	Enabled    [IM2NumPeriph]bool
	DMAEnabled [IM2NumPeriph]bool
	StatusClr  [IM2NumPeriph]bool
	Per        [IM2NumPeriph]im2PeripheralState
	IntN       bool
	Vector     int
	Ieo        bool
	Status     [IM2NumPeriph]bool
}

// StateID identifies the driver in a captured machine state.
func (d *IM2Driver) StateID() string { return "next.im2" }

// SaveState captures the driver and its chain.
func (d *IM2Driver) SaveState() []byte {
	s := im2State{
		HWIM2: d.hwIM2, VectorBase: d.vectorBase,
		Req: d.req, Enabled: d.enabled, DMAEnabled: d.dmaEnabled, StatusClr: d.statusClr,
		IntN: d.chain.intN, Vector: d.chain.vector, Ieo: d.chain.ieo, Status: d.chain.status,
	}
	for i, p := range d.chain.per {
		s.Per[i] = im2PeripheralState{p.intReqD, p.im2IntReq, p.intStatus, p.isrServicedD, int(p.state)}
	}
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(s); err != nil {
		panic(fmt.Sprintf("im2: encode state: %v", err))
	}
	return buf.Bytes()
}

// LoadState restores the driver and its chain.
func (d *IM2Driver) LoadState(data []byte) error {
	var s im2State
	if err := gob.NewDecoder(bytes.NewReader(data)).Decode(&s); err != nil {
		return fmt.Errorf("im2: decode state: %w", err)
	}
	for i, p := range s.Per {
		if p.State < int(im2S0) || p.State > int(im2ISR) {
			return fmt.Errorf("im2: peripheral %d: bad state %d", i, p.State)
		}
	}
	if s.Vector < 0 || s.Vector > 0x0F {
		return fmt.Errorf("im2: bad vector %d", s.Vector)
	}
	d.hwIM2, d.vectorBase = s.HWIM2, s.VectorBase
	d.req, d.enabled, d.dmaEnabled, d.statusClr = s.Req, s.Enabled, s.DMAEnabled, s.StatusClr
	d.chain.intN, d.chain.vector, d.chain.ieo, d.chain.status = s.IntN, s.Vector, s.Ieo, s.Status
	for i, p := range s.Per {
		d.chain.per[i] = im2Peripheral{
			intReqD: p.IntReqD, im2IntReq: p.Im2IntReq, intStatus: p.IntStatus,
			isrServicedD: p.IsrServicedD, state: im2DevState(p.State),
		}
	}
	return nil
}
