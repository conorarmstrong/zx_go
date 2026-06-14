// Package esxdos implements the esxDOS RST 8 API used by NextZXOS and
// dot commands. RST 8 reads the inline parameter byte at PC+1 to
// select the API, dispatches through a handler table and returns with
// PC adjusted past the parameter byte. Sprint 3 lands a skeleton with
// enough handlers to clear boot; Sprint 4 fills in F_OPEN, F_READ,
// F_WRITE, F_SEEK, F_GETPOS, F_FSTAT, F_OPENDIR, F_READDIR and the
// dot-command loader.
package esxdos
