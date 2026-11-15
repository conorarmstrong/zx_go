package ctc

import (
	"bytes"
	"encoding/gob"
	"fmt"
)

// Complete state capture for the four CTC channels, for rewind and save
// states. Every Channel field is captured, including the delayed edge-detect
// levels and the trigger input, because a capture can land between a
// SetTrigger and the next Tick.

type channelState struct {
	State      int
	ControlReg uint8
	TimeConst  uint8
	PCount     uint8
	TCount     uint8
	ZcToD      bool
	ClkTrgD    bool
	IowrD      bool
	ClkTrg     bool
}

type bankState struct {
	Ch [NumChannels]channelState
}

// StateID identifies the bank in a captured machine state.
func (b *Bank) StateID() string { return "next.ctc" }

// SaveState captures all four channels.
func (b *Bank) SaveState() []byte {
	var s bankState
	for i, c := range b.ch {
		s.Ch[i] = channelState{
			State: int(c.state), ControlReg: c.controlReg, TimeConst: c.timeConst,
			PCount: c.pCount, TCount: c.tCount, ZcToD: c.zcToD,
			ClkTrgD: c.clkTrgD, IowrD: c.iowrD, ClkTrg: c.clkTrg,
		}
	}
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(s); err != nil {
		panic(fmt.Sprintf("ctc: encode state: %v", err))
	}
	return buf.Bytes()
}

// LoadState restores all four channels.
func (b *Bank) LoadState(data []byte) error {
	var s bankState
	if err := gob.NewDecoder(bytes.NewReader(data)).Decode(&s); err != nil {
		return fmt.Errorf("ctc: decode state: %w", err)
	}
	for i := range s.Ch {
		if s.Ch[i].State < int(sReset) || s.Ch[i].State > int(sRunTC) {
			return fmt.Errorf("ctc: channel %d: bad state %d", i, s.Ch[i].State)
		}
	}
	for i, cs := range s.Ch {
		c := b.ch[i]
		c.state, c.controlReg, c.timeConst = state(cs.State), cs.ControlReg, cs.TimeConst
		c.pCount, c.tCount, c.zcToD = cs.PCount, cs.TCount, cs.ZcToD
		c.clkTrgD, c.iowrD, c.clkTrg = cs.ClkTrgD, cs.IowrD, cs.ClkTrg
	}
	return nil
}
