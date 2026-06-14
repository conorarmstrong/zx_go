// Package next is the umbrella for ZX Spectrum Next emulation. The
// real subsystems live in the child packages (nextregs, layer2,
// sprite, tilemap, copper, dma, dac, divmmc, esxdos, sdcard, nex,
// rtc, uart, compositor); this file exists so the umbrella import
// path is discoverable and `go doc` has somewhere to land.
//
// Implementation roadmap, current state and remaining work live in
// ROADMAP.md at the repo root.
package next
