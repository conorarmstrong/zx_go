// Package nex implements the .NEX V1.2 file loader: 512-byte header,
// optional palette (512 bytes), optional screen, optional Copper
// program (2 KB), then 16K bank blocks in the documented order
// [5,2,0,1,3,4,6,7,8,9,...,111].
package nex
