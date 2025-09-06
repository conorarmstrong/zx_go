# ZX Spectrum Emulator - Extended Features

## Overview
This ZX Spectrum emulator has been extended with comprehensive support for multiple ZX Spectrum models and peripheral hardware interfaces.

## Supported ZX Spectrum Models
- **ZX Spectrum 48K**: Original 48K model with 16KB ROM
- **ZX Spectrum 128K**: Enhanced model with 128KB RAM and dual ROM banks
- **ZX Spectrum +2**: Improved model with built-in cassette recorder
- **ZX Spectrum +2A**: Enhanced +2 with improved hardware
- **ZX Spectrum +3**: Final official model with built-in 3" disk drive

### Model Switching
- Switch between different Spectrum models via the **Machine** menu
- Each model loads its appropriate ROM files and configures memory correctly
- Real-time model switching without restarting the emulator

## ROM Management System
The emulator includes a sophisticated ROM management system that automatically:
- Loads appropriate ROMs for each Spectrum model
- Manages memory mapping for different configurations
- Provides fallback mechanisms for missing ROM files
- Displays ROM information and status

### Available ROM Files
- `48.rom` - 48K Spectrum ROM
- `128-0.rom`, `128-1.rom` - 128K Spectrum ROMs
- `plus2-0.rom`, `plus2-1.rom` - +2 model ROMs
- `plus3-0.rom`, `plus3-1.rom`, `plus3-2.rom`, `plus3-3.rom` - +3 model ROMs
- `trdos.rom` - TR-DOS (Beta Disk Interface)
- `pentagon-0.rom` - Pentagon (Soviet ZX Spectrum clone)

## Peripheral Hardware Support

### DISCiPLE Disk Interface
The Miles Gordon Technology DISCiPLE disk interface is fully supported:

#### Features
- **8KB GDOS ROM**: Operating system for disk operations
- **8KB RAM**: Interface working memory
- **Disk Controller**: WD1770-compatible floppy disk controller
- **I/O Port Support**: Complete port mapping for disk operations
- **MGT Disk Format**: Support for .mgt disk image files (placeholder)

#### Hardware Specifications
- **Ports**: 0x1F-0x8F (Control, Status, Track, Sector, Data, Drive, NMI, System)
- **Commands**: Restore, Seek, Step, Read/Write Sector, Force Interrupt
- **Features**: Red button (NMI), Inhibit button, Snapshot functionality
- **Storage**: Up to 16MB across 2 drives

#### Usage
- Enable via **Peripherals > Enable Disciple**
- Disable via **Peripherals > Disable Disciple**
- Check status via **Emulator > Peripheral Status**

### Multiface Interface
Full support for Romantic Robot Multiface variants:

#### Supported Variants
- **Multiface 1**: Original version for 48K Spectrum
- **Multiface 128**: Enhanced version for 128K/+2 models
- **Multiface 3**: Advanced version for +2A/+3 models

#### Features
- **8KB ROM**: Interface software including debugger
- **8KB RAM**: Working memory for operations
- **Red Button**: Non-maskable interrupt (NMI) activation
- **Stealth Mode**: Invisible operation to avoid detection
- **Memory Snapshot**: Save/load complete memory states
- **Video Page Store**: Enhanced memory management (128/3 variants)

#### Hardware Specifications
- **ROM Paging**: Triggered by NMI vector access (0x0066/0x0067)
- **I/O Ports**: Decoded pattern xxxx xxxx x011 x1xx
- **Memory Mapping**: ROM at 0x0000-0x1FFF when active
- **Control Features**: Page in/out, stealth mode, red button control

#### Usage
- Enable variants via **Peripherals > Enable Multiface [1/128/3]**
- Disable via **Peripherals > Disable Multiface**
- Red button simulation automatically handled
- Check status via **Emulator > Peripheral Status**

### Peripheral Manager
Centralized management of all peripheral devices:
- **Automatic Integration**: Seamless integration with CPU and memory systems
- **I/O Port Handling**: Proper port decode and data routing
- **Memory Interception**: ROM paging and RAM access control
- **Status Monitoring**: Real-time status of all connected peripherals

## Technical Implementation

### Memory Architecture
- **RAM Banks**: 8 × 16KB pages for 128K models
- **ROM Banks**: 4 × 16KB pages for different models
- **Memory Mapping**: Dynamic page mapping with peripheral support
- **Banking Control**: Full support for 128K memory banking

### I/O System
- **Port Decoding**: Hardware-accurate port address decoding
- **Peripheral Chaining**: Multiple peripherals can coexist
- **Interrupt Handling**: NMI support for Multiface red button
- **Hardware Timing**: Accurate T-state timing for operations

### ROM Loading
- **Automatic Detection**: Finds and loads appropriate ROM files
- **Error Handling**: Graceful fallback for missing ROMs
- **Placeholder ROMs**: Minimal working ROMs when originals unavailable
- **Format Validation**: Ensures ROM files are correct size and format

## User Interface Enhancements

### Menu System
- **Machine**: Select ZX Spectrum model (48K/128K/+2/+2A/+3)
- **Peripherals**: Enable/disable disk and multiface interfaces
- **File**: Load ROMs and snapshots (existing functionality)
- **Emulator**: Control emulation and view status information

### Status Information
- **ROM Info**: Display loaded ROMs and current model
- **Peripheral Status**: Show state of all connected peripherals
- **Real-time Updates**: Live status updates as you interact with hardware

## Development Notes

### Code Organization
- `pkg/roms/`: ROM management system
- `pkg/memory/`: Enhanced memory management with model support  
- `pkg/disciple/`: DISCiPLE disk interface implementation
- `pkg/multiface/`: Multiface interface implementation
- `pkg/peripherals/`: Peripheral management system

### Future Extensions
The architecture supports easy addition of:
- Interface 1 (RS232/Network)
- Interface 2 (ROM cartridges/Joystick)
- Kempston Joystick
- Sound interfaces (AY-3-8910)
- Additional disk interfaces (Beta Disk, +D, etc.)

### Compatibility
- Full backward compatibility with existing 48K/128K software
- Hardware-level emulation ensures maximum compatibility
- Proper timing and memory mapping for authentic behavior

## Usage Examples

### Basic Usage
1. Launch emulator (defaults to 48K mode)
2. Select desired Spectrum model from **Machine** menu
3. Enable peripherals as needed from **Peripherals** menu
4. Load software via **File** menu or use built-in BASIC

### Peripheral Configuration
1. For disk operations: Enable DISCiPLE interface
2. For debugging/snapshots: Enable appropriate Multiface variant
3. Check **Peripheral Status** to verify configuration
4. Use **ROM Info** to confirm all ROMs loaded correctly

### Model-Specific Features
- **48K**: Original BASIC and simple memory layout
- **128K/+2**: Enhanced BASIC with music and extra memory
- **+2A/+3**: Advanced features with CP/M compatibility (with appropriate software)

This extended emulator provides an authentic ZX Spectrum experience with comprehensive hardware support for the most popular peripherals and models.