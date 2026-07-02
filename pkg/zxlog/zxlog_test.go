package zxlog

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/conorarmstrong/zx_go/pkg/version"
)

// fixedTime makes record timestamps deterministic so the formatted
// output is testable.
func fixedTime() time.Time {
	return time.Date(2026, 5, 16, 13, 42, 1, 234000000, time.UTC)
}

func TestHandlerFormatsLevelAndAttrs(t *testing.T) {
	var buf bytes.Buffer
	h := newHandler(&buf, slog.LevelDebug, false /* no colors */)
	rec := slog.NewRecord(fixedTime(), slog.LevelWarn, "memory bus underrun", 0)
	rec.AddAttrs(slog.Int("bank", 7), slog.String("source", "ula"))
	if err := h.Handle(context.Background(), rec); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	got := buf.String()
	for _, want := range []string{
		"13:42:01.234",
		"WARN",
		"memory bus underrun",
		"bank=7",
		"source=ula",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q\n  got: %s", want, got)
		}
	}
}

func TestHandlerLevelFilter(t *testing.T) {
	var buf bytes.Buffer
	h := newHandler(&buf, slog.LevelWarn, false)
	if h.Enabled(context.Background(), slog.LevelInfo) {
		t.Errorf("Info should be filtered out when level=Warn")
	}
	if !h.Enabled(context.Background(), slog.LevelWarn) {
		t.Errorf("Warn should pass at level=Warn")
	}
	if !h.Enabled(context.Background(), slog.LevelError) {
		t.Errorf("Error should pass at level=Warn")
	}
}

func TestHandlerDefaultLevelTag(t *testing.T) {
	var buf bytes.Buffer
	h := newHandler(&buf, slog.LevelDebug, false)
	// Custom level outside Debug/Info/Warn/Error → hits the default
	// arm and renders as LVnnn.
	rec := slog.NewRecord(fixedTime(), slog.Level(12), "custom", 0)
	if err := h.Handle(context.Background(), rec); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	got := buf.String()
	if !strings.Contains(got, "LV12") {
		t.Errorf("custom level not rendered as LVnn: %q", got)
	}
}

func TestNewHandlerColoredPalette(t *testing.T) {
	var buf bytes.Buffer
	h := newHandler(&buf, slog.LevelDebug, true)
	if h.pal.reset == "" {
		t.Error("colored=true should select on palette, got off")
	}
	h2 := newHandler(&buf, slog.LevelDebug, false)
	if h2.pal.reset != "" {
		t.Error("colored=false should select off palette")
	}
}

func TestWithAttrsAndWithGroupAreNoOps(t *testing.T) {
	var buf bytes.Buffer
	h := newHandler(&buf, slog.LevelDebug, false)
	got := h.WithAttrs([]slog.Attr{slog.String("ignored", "value")})
	if got != h {
		t.Error("WithAttrs should return the same handler instance")
	}
	got2 := h.WithGroup("ignored")
	if got2 != h {
		t.Error("WithGroup should return the same handler instance")
	}
}

func TestSetupReturnsLoggerAndSetsDefault(t *testing.T) {
	old := slog.Default()
	defer slog.SetDefault(old)
	logger := Setup(slog.LevelDebug)
	if logger == nil {
		t.Fatal("Setup returned nil logger")
	}
	if slog.Default() == nil {
		t.Error("Setup did not set default logger")
	}
}

func TestBannerNoTTYIsNoop(t *testing.T) {
	// os.Stderr in test runner is typically not a TTY, so Banner
	// should silently return without writing.
	Banner() // no observable side-effect to assert; just ensure it does not panic
}

func TestStdLogBridgeStripsNewline(t *testing.T) {
	var buf bytes.Buffer
	h := newHandler(&buf, slog.LevelDebug, false)
	saved := timeNow
	timeNow = fixedTime
	defer func() { timeNow = saved }()

	b := stdLogBridge{h: h}
	if _, err := b.Write([]byte("hello world\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got := buf.String()
	if strings.Contains(got, "world\n\n") {
		t.Errorf("trailing newline not stripped: %q", got)
	}
	if !strings.Contains(got, "hello world") {
		t.Errorf("message body lost: %q", got)
	}
	if !strings.Contains(got, "INFO") {
		t.Errorf("bridge should map log.Printf to INFO: %q", got)
	}
}

// TestRenderBannerContainsMetadata exercises the banner-rendering body
// directly since Banner() itself no-ops under `go test` (stderr isn't a TTY).
func TestRenderBannerContainsMetadata(t *testing.T) {
	var buf bytes.Buffer
	renderBanner(&buf)
	out := buf.String()
	for _, want := range []string{
		"a ZX Spectrum emulator in Go", // tagline
		Version,                        // version
		"built ",                       // build-date prefix
		version.Date(),                 // the build date itself
		Author,                         // subtitle author
		RepoURL,                        // subtitle repo
	} {
		if !strings.Contains(out, want) {
			t.Errorf("renderBanner output missing %q\n---\n%s", want, out)
		}
	}
	// Six art rows + 1 leading blank + 2 subtitle + 1 trailing blank.
	if got := strings.Count(out, "\n"); got < 9 {
		t.Errorf("renderBanner produced %d lines, want >= 9", got)
	}
}
