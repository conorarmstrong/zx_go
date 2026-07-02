package rtc

import (
	"log/slog"
	"os"
)

// ZX_GO_RTC_TRACE: diagnostic — log every DS1307 register read with a
// running counter, so a guest clock-fetch retry storm is visible as
// clustered re-reads.
var traceReads = os.Getenv("ZX_GO_RTC_TRACE") != ""

var traceCount uint64

func traceRead(reg byte) {
	traceCount++
	slog.Info("rtc-read", "reg", reg, "n", traceCount)
}
