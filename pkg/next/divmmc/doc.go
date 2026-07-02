// Package divmmc implements the Spectrum Next's divMMC SD-card
// controller and its automatic ROM-paging behaviour on M1 fetches at
// 0x0000, 0x0008, 0x0038, 0x0066, 0x04C6 and 0x0562. divMMC ROM maps
// at 0x0000-0x1FFF (or divMMC RAM bank 3 when MAPRAM is latched) and
// divMMC RAM at 0x2000-0x3FFF while paged in; unpaging fires when PC
// leaves 0x0000-0x3FFF, via the generalised M1-PC trap.
package divmmc
