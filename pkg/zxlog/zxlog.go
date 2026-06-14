// Package zxlog wires the ZX_Go startup console output: a colored
// banner and a log/slog handler that formats events as
//
//	HH:MM:SS.sss LEVEL message  key=value …
//
// using ANSI colors when stderr is a TTY. Plain output is used when
// stderr is a file or pipe so log captures stay readable.
//
// Standard log/slog levels are used (Debug / Info / Warn / Error).
// "NOTE" maps to Info — that's the conventional slog name for the
// non-warning informational level.
package zxlog

import (
	"context"
	"fmt"
	"io"
	"log"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/conorarmstrong/zx_go/pkg/version"
	"golang.org/x/term"
)

// ANSI colors. Empty strings when colors are disabled.
type palette struct {
	reset, dim, bold               string
	red, yellow, green, cyan, gray string
	// Spectrum stripe rainbow (used by the banner).
	stripeRed, stripeYellow, stripeGreen, stripeCyan, stripeBlue, stripeMagenta string
}

// Version is the human-readable release tag shown in the startup
// banner. It defaults to the canonical release string in
// pkg/version (the single source of truth) but stays a var so a
// build can still override it with:
//
//	go build -ldflags "-X github.com/conorarmstrong/zx_go/pkg/zxlog.Version=v0.4.0"
var Version = version.Version

// Author and RepoURL are surfaced in the startup banner. Sourced
// from pkg/version (single source of truth, shared with Help → About).
var (
	Author  = version.Author
	RepoURL = version.RepoURL
)

var (
	off = palette{}
	on  = palette{
		reset:         "\x1b[0m",
		dim:           "\x1b[2m",
		bold:          "\x1b[1m",
		red:           "\x1b[31m",
		yellow:        "\x1b[33m",
		green:         "\x1b[32m",
		cyan:          "\x1b[36m",
		gray:          "\x1b[90m",
		stripeRed:     "\x1b[38;5;196m",
		stripeYellow:  "\x1b[38;5;226m",
		stripeGreen:   "\x1b[38;5;46m",
		stripeCyan:    "\x1b[38;5;51m",
		stripeBlue:    "\x1b[38;5;21m",
		stripeMagenta: "\x1b[38;5;201m",
	}
)

// Setup wires log/slog with a colored handler writing to stderr.
// Subsequent slog.Debug / slog.Info / slog.Warn / slog.Error calls
// use this handler.
//
// The standard library's "log" package is also retargeted at slog
// so existing log.Printf / log.Println calls scattered through the
// codebase show up correctly. Existing callers don't need to be
// migrated to slog all at once — log.Printf goes through slog.Info,
// log.Fatalf still terminates the process.
//
// Returns the underlying slog.Logger so callers that want their own
// scoped logger can derive one via logger.With(...).
func Setup(level slog.Level) *slog.Logger {
	w := os.Stderr
	useColor := term.IsTerminal(int(w.Fd()))
	h := newHandler(w, level, useColor)
	logger := slog.New(h)
	slog.SetDefault(logger)
	// Bridge the standard "log" package into slog so legacy
	// log.Printf calls inherit the formatting and color scheme.
	log.SetFlags(0)
	log.SetPrefix("")
	log.SetOutput(stdLogBridge{h: h})
	return logger
}

// stdLogBridge forwards anything written via the std log package
// into the slog handler at INFO level. The trailing newline that
// log.Printf adds is stripped so it doesn't double up.
type stdLogBridge struct{ h slog.Handler }

func (b stdLogBridge) Write(p []byte) (int, error) {
	msg := strings.TrimRight(string(p), "\n")
	rec := slog.NewRecord(timeNow(), slog.LevelInfo, msg, 0)
	_ = b.h.Handle(context.Background(), rec)
	return len(p), nil
}

// Banner prints the ZX_Go startup banner to stderr in Spectrum
// rainbow stripes. No-op when stderr is not a TTY.
func Banner() {
	w := os.Stderr
	if !term.IsTerminal(int(w.Fd())) {
		return
	}
	renderBanner(w)
}

// renderBanner writes the rainbow startup banner to w. Split out
// from Banner so the rendering can be exercised without a TTY (the
// os.Stderr TTY gate makes Banner itself a no-op under `go test`).
func renderBanner(w io.Writer) {
	p := on
	// 5-line ASCII art for "ZX_Go" with Spectrum rainbow stripes
	// on each row. Each stripe row uses a different palette colour
	// so the banner shimmers like a power-on screen.
	// "ZX Go" — uppercase Z, X, G; lowercase o (shorter, only
	// visible in the bottom four rows, leaving the top two blank
	// where an uppercase 'O' would otherwise have its top arc).
	lines := []string{
		`███████╗██╗  ██╗     ██████╗          `,
		`╚══███╔╝╚██╗██╔╝    ██╔════╝          `,
		`  ███╔╝  ╚███╔╝     ██║  ███╗ ██████╗ `,
		` ███╔╝   ██╔██╗     ██║   ██║██╔═══██╗`,
		`███████╗██╔╝ ██╗    ╚██████╔╝╚██████╔╝`,
		`╚══════╝╚═╝  ╚═╝     ╚═════╝  ╚═════╝ `,
	}
	colors := []string{p.stripeRed, p.stripeYellow, p.stripeGreen, p.stripeCyan, p.stripeBlue, p.stripeMagenta}
	_, _ = fmt.Fprintln(w)
	for i, ln := range lines {
		_, _ = fmt.Fprintf(w, "  %s%s%s\n", colors[i%len(colors)], ln, p.reset)
	}
	// Subtitle: tagline left-aligned, then the version and the build
	// date so the line reads like a footer.
	tagline := "a ZX Spectrum emulator in Go"
	_, _ = fmt.Fprintf(w, "  %s%s%s   %s%s%s %s(built %s)%s\n",
		p.bold, tagline, p.reset,
		p.cyan, Version, p.reset,
		p.gray, version.Date(), p.reset)
	// Author + repo on a second subtitle line.
	_, _ = fmt.Fprintf(w, "  %s%s%s · %s%s%s\n",
		p.gray, Author, p.reset,
		p.gray, RepoURL, p.reset)
	_, _ = fmt.Fprintln(w)
}

// handler is a small slog.Handler that prints one line per event.
type handler struct {
	w       io.Writer
	mu      *sync.Mutex
	level   slog.Level
	pal     palette
	colored bool
}

func newHandler(w io.Writer, level slog.Level, colored bool) *handler {
	p := off
	if colored {
		p = on
	}
	return &handler{w: w, mu: &sync.Mutex{}, level: level, pal: p, colored: colored}
}

func (h *handler) Enabled(_ context.Context, l slog.Level) bool { return l >= h.level }

func (h *handler) Handle(_ context.Context, r slog.Record) error {
	var levelTag, levelCol string
	switch r.Level {
	case slog.LevelDebug:
		levelTag, levelCol = "DEBUG", h.pal.gray
	case slog.LevelInfo:
		levelTag, levelCol = "INFO ", h.pal.green
	case slog.LevelWarn:
		levelTag, levelCol = "WARN ", h.pal.yellow
	case slog.LevelError:
		levelTag, levelCol = "ERROR", h.pal.red
	default:
		levelTag, levelCol = fmt.Sprintf("LV%-3d", int(r.Level)), h.pal.cyan
	}

	t := r.Time.Format("15:04:05.000")
	var b strings.Builder
	fmt.Fprintf(&b, "%s%s%s %s%s%s %s",
		h.pal.gray, t, h.pal.reset,
		levelCol, levelTag, h.pal.reset,
		r.Message)
	r.Attrs(func(a slog.Attr) bool {
		fmt.Fprintf(&b, " %s%s%s=%v", h.pal.cyan, a.Key, h.pal.reset, a.Value.Any())
		return true
	})
	b.WriteByte('\n')

	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := io.WriteString(h.w, b.String())
	return err
}

func (h *handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	// Group/attr scoping is uncommon in this codebase; keeping the
	// implementation small means we just ignore scope additions.
	// If usage grows, switch to slog's TextHandler underneath.
	_ = attrs
	return h
}

func (h *handler) WithGroup(name string) slog.Handler { _ = name; return h }

// timeNow is a var so tests can override it. Production code uses
// time.Now indirectly.
var timeNow = func() time.Time { return time.Now() }
