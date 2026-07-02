package rzx

import (
	"fmt"

	"github.com/conorarmstrong/zx_go/pkg/snapshot"
)

// EncodeSnapshot wraps a snapshot.Snapshot in an RZX SnapshotBlock by
// asking the snapshot package to serialise itself in the requested
// format. The on-the-wire format identifier (the 4-byte string inside
// the RZX block) is derived from the format argument; "Z80", "SZX",
// and "SNA" are accepted in any case. The Automatic flag controls
// whether the resulting block is treated as an autosave (intermediate
// rollback point) or a user-driven snapshot.
func EncodeSnapshot(snap *snapshot.Snapshot, formatName string, automatic bool) (*SnapshotBlock, error) {
	format, err := parseSnapshotFormat(formatName)
	if err != nil {
		return nil, err
	}
	data, err := snap.SaveBytes(format)
	if err != nil {
		return nil, fmt.Errorf("rzx: snapshot encode: %w", err)
	}
	return &SnapshotBlock{
		Format:    canonicalFormatName(format),
		Data:      data,
		Automatic: automatic,
	}, nil
}

// DecodeSnapshot is the inverse of EncodeSnapshot: it reads the format
// identifier off the SnapshotBlock and asks the snapshot package to
// parse the embedded bytes. External-link snapshot blocks (which RZX
// can carry but the parser intentionally leaves with empty Data — see
// readSnapshot) cannot be decoded and produce an error.
func DecodeSnapshot(block *SnapshotBlock) (*snapshot.Snapshot, error) {
	if block == nil {
		return nil, fmt.Errorf("rzx: nil snapshot block")
	}
	if len(block.Data) == 0 {
		return nil, fmt.Errorf("rzx: snapshot block has no embedded data (external link?)")
	}
	format, err := parseSnapshotFormat(block.Format)
	if err != nil {
		return nil, err
	}
	snap := snapshot.New()
	if err := snap.LoadBytes(block.Data, format); err != nil {
		return nil, fmt.Errorf("rzx: snapshot decode: %w", err)
	}
	return snap, nil
}

// parseSnapshotFormat maps an RZX format identifier to the snapshot
// package's enum. Accepts the canonical uppercase identifiers ("Z80",
// "SZX", "SNA") or their all-lowercase equivalents; trailing NULs are
// already stripped by nullTerminated on the read path.
func parseSnapshotFormat(name string) (snapshot.SnapshotFormat, error) {
	switch name {
	case SnapshotFormatZ80, "z80":
		return snapshot.FormatZ80, nil
	case SnapshotFormatSZX, "szx":
		return snapshot.FormatSZX, nil
	case SnapshotFormatSNA, "sna":
		return snapshot.FormatSNA, nil
	}
	return snapshot.FormatUnknown, fmt.Errorf("rzx: unsupported snapshot format %q", name)
}

// canonicalFormatName returns the 3-character RZX-on-disk identifier
// for a snapshot format enum. Used by EncodeSnapshot so the resulting
// block carries the correct identifier regardless of how the caller
// spelled it.
func canonicalFormatName(f snapshot.SnapshotFormat) string {
	switch f {
	case snapshot.FormatZ80:
		return SnapshotFormatZ80
	case snapshot.FormatSZX:
		return SnapshotFormatSZX
	case snapshot.FormatSNA:
		return SnapshotFormatSNA
	}
	return ""
}
