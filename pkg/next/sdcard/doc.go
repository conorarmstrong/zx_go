// Package sdcard mounts a FAT16/FAT32 disk image as the Spectrum Next's
// SD card. Reads and writes go through the in-process file system layer
// to a host-side .img file; no kernel modules or FUSE mounts are
// involved. It wires into the esxDOS handlers.
package sdcard
