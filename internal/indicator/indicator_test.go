package indicator

import (
	"os"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestCalculateMinHeightClampsZero(t *testing.T) {
	if got := calculateMinHeight(0); got != 0 {
		t.Fatalf("calculateMinHeight(0) = %d, want 0", got)
	}
	if got := calculateMinHeight(1); got != 0 {
		t.Fatalf("calculateMinHeight(1) = %d, want 0", got)
	}
	if got := calculateMinHeight(24); got != 23 {
		t.Fatalf("calculateMinHeight(24) = %d, want 23", got)
	}
}

func TestWrapLinePreservesUTF8Runes(t *testing.T) {
	line := "↑ 128 KiB/s | ↓ 256 KiB/s | Σ ⬢测试"
	parts := wrapLine(line, 5)
	for i, part := range parts {
		if !utf8.ValidString(part) {
			t.Fatalf("part %d (%x) contains invalid UTF-8", i, part)
		}
		if n := utf8.RuneCountInString(part); n > 5 {
			t.Fatalf("part %d has %d runes, want <= 5", i, n)
		}
	}
	if got := strings.Join(parts, ""); got != line {
		t.Fatalf("rejoined = %q, want %q", got, line)
	}
}

func TestIndicatorCloseIsIdempotent(t *testing.T) {
	sw := &Indicator{
		resizeChan: make(chan os.Signal, 1),
	}
	if err := sw.Close(); err != nil {
		t.Fatalf("first Close() error = %v", err)
	}
	if err := sw.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
}
