# Z80 Instruction Set Completion Matrix

This document tracks the implementation status of all Z80 instructions.

## DD Prefix Instructions (IX operations)

### Implemented ✅
- 0x09: ADD IX,BC
- 0x19: ADD IX,DE  
- 0x21: LD IX,nn
- 0x22: LD (nn),IX
- 0x23: INC IX
- 0x29: ADD IX,IX
- 0x2A: LD IX,(nn)
- 0x2B: DEC IX
- 0x34: INC (IX+d)
- 0x35: DEC (IX+d)
- 0x36: LD (IX+d),n
- 0x39: ADD IX,SP
- 0x46: LD B,(IX+d)
- 0x4E: LD C,(IX+d)
- 0x56: LD D,(IX+d)
- 0x5E: LD E,(IX+d)
- 0x66: LD H,(IX+d)
- 0x6E: LD L,(IX+d)
- 0x70: LD (IX+d),B
- 0x71: LD (IX+d),C
- 0x72: LD (IX+d),D
- 0x73: LD (IX+d),E
- 0x74: LD (IX+d),H
- 0x75: LD (IX+d),L
- 0x77: LD (IX+d),A
- 0x7E: LD A,(IX+d)
- 0x86: ADD A,(IX+d)
- 0x8E: ADC A,(IX+d)
- 0x96: SUB (IX+d)
- 0x9E: SBC A,(IX+d)
- 0xA6: AND (IX+d)
- 0xAE: XOR (IX+d)
- 0xB6: OR (IX+d)
- 0xBE: CP (IX+d)
- 0xCB: DD CB prefix (bit operations)
- 0xE1: POP IX
- 0xE3: EX (SP),IX
- 0xE5: PUSH IX
- 0xE9: JP (IX)

## FD Prefix Instructions (IY operations)

### Implemented ✅ (Systematic Implementation Complete)
- 0x09: ADD IY,BC
- 0x19: ADD IY,DE  
- 0x21: LD IY,nn
- 0x22: LD (nn),IY
- 0x23: INC IY
- 0x29: ADD IY,IY
- 0x2A: LD IY,(nn)
- 0x2B: DEC IY
- 0x34: INC (IY+d)
- 0x35: DEC (IY+d)
- 0x36: LD (IY+d),n
- 0x39: ADD IY,SP
- 0x46: LD B,(IY+d)
- 0x4E: LD C,(IY+d)
- 0x56: LD D,(IY+d)
- 0x5E: LD E,(IY+d)
- 0x66: LD H,(IY+d)
- 0x6E: LD L,(IY+d)
- 0x70: LD (IY+d),B
- 0x71: LD (IY+d),C
- 0x72: LD (IY+d),D
- 0x73: LD (IY+d),E
- 0x74: LD (IY+d),H
- 0x75: LD (IY+d),L
- 0x77: LD (IY+d),A
- 0x7E: LD A,(IY+d)
- 0x86: ADD A,(IY+d)
- 0x8E: ADC A,(IY+d)
- 0x96: SUB (IY+d)
- 0x9E: SBC A,(IY+d)
- 0xA6: AND (IY+d)
- 0xAE: XOR (IY+d)
- 0xB6: OR (IY+d)
- 0xBE: CP (IY+d)
- 0xCB: FD CB prefix (bit operations)
- 0xE1: POP IY
- 0xE3: EX (SP),IY
- 0xE5: PUSH IY
- 0xE9: JP (IY)
- 0xF9: LD SP,IY

## ED Prefix Instructions (Extended instructions)

### Implemented ✅
- 0x40: IN B,(C)
- 0x41: OUT (C),B
- 0x42: SBC HL,BC
- 0x43: LD (nn),BC
- 0x44: NEG
- 0x45: RETN
- 0x46: IM 0
- 0x47: LD I,A
- 0x48: IN C,(C)
- 0x49: OUT (C),C
- 0x4B: LD BC,(nn)
- 0x4D: RETI
- 0x4F: LD R,A
- 0x50: IN D,(C)
- 0x51: OUT (C),D
- 0x52: SBC HL,DE
- 0x53: LD (nn),DE
- 0x56: IM 1
- 0x57: LD A,I
- 0x58: IN E,(C)
- 0x59: OUT (C),E
- 0x5B: LD DE,(nn)
- 0x5E: IM 2
- 0x5F: LD A,R
- 0x60: IN H,(C)
- 0x61: OUT (C),H
- 0x62: SBC HL,HL
- 0x68: IN L,(C)
- 0x69: OUT (C),L
- 0x70: IN F,(C)
- 0x71: OUT (C),0
- 0x72: SBC HL,SP
- 0x73: LD (nn),SP
- 0x78: IN A,(C)
- 0x79: OUT (C),A
- 0x7B: LD SP,(nn)
- 0xA0: LDI
- 0xA8: LDD
- 0xB0: LDIR
- 0xB8: LDDR

### To be implemented
- `ADC HL,rr` instructions (0x4A, 0x5A, 0x6A, 0x7A)
- Block search instructions (CPI, CPIR, CPD, CPDR)
- Block output instructions (OUTI, OTIR, OUTD, OTDR)
- RRD and RLD instructions
- `LD HL,(nn)` (0x6B) and `LD (nn),HL` (0x63)
- Several NOP instructions (e.g. 0x4C, 0x54, 0x5C, 0x64, 0x6C, 0x74, 0x7C)

## CB Prefix Instructions (Bit operations)

### Status: ✅ Systematic Implementation Complete
Bit manipulation, rotate, shift operations on all registers and (HL).

## Main Opcode Matrix (0x00-0xFF)

### Status: MOSTLY COMPLETE
Standard 8080 compatible instructions are implemented.

## Comprehensive Implementation Strategy

1. **Complete DD/FD Mapping**: Ensure IX/IY instructions are symmetric
2. **Verify ED Instructions**: All block, I/O, and extended operations
3. **Validate CB Operations**: All bit/shift operations on registers and memory
4. **Add Missing Timings**: Ensure all T-state counts are accurate
5. **Test Coverage**: Create test suite for instruction validation

## Priority Missing Instructions (discovered in real usage)

Based on actual program execution, these were missing:
- DD 0x73: LD (IX+d),E ✅ FIXED
- DD 0xAE: XOR (IX+d) ✅ FIXED
- DD 0x4E: LD C,(IX+d) ✅ FIXED
- DD 0x86: ADD A,(IX+d) ✅ FIXED

## Next Steps

1. Systematically verify FD instruction completeness
2. Create comprehensive ED instruction coverage
3. Validate all CB instruction variants
4. Add automated test suite for instruction verification