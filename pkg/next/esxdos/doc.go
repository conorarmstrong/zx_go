// Package esxdos implements the esxDOS RST 8 API used by NextZXOS and
// dot commands. RST 8 reads the inline parameter byte at PC+1 to
// select the API, dispatches through a handler table and returns with
// PC adjusted past the parameter byte. Handlers cover the boot-path
// minimum (M_GETHANDLE, M_DRVAPI) plus the file API (F_OPEN, F_READ,
// F_WRITE, F_SEEK, F_GETPOS, F_FSTAT, F_OPENDIR, F_READDIR).
package esxdos
