package indicator

import (
	"bytes"
	"fmt"
	"os"
	"os/signal"
	"sync"

	"golang.org/x/term"
)

var ansiNewlineByte = []byte{'\n'}

const (
	ansiCarriageReturn  = "\r"
	escapeSeqClear      = "\033[H\033[J"
	escapeSeqClearLine  = "\033[2K"
	escapeSeqHideCursor = "\033[?25l"
	escapeSeqShowCursor = "\033[?25h"
)

const StatusMarginTopLines = 1

// Indicator captures log output and manages the status line display
type Indicator struct {
	mu         sync.Mutex
	statusLine string
	logBuffer  []string
	maxLogs    int
	resizeChan chan os.Signal
}

func calculateMinHeight(heigh int) int {
	return heigh - StatusMarginTopLines
}

// NewIndicator creates a writer that captures logs and manages a status line
func NewIndicator() *Indicator {
	// Get the current terminal size
	_, height := getTerminalSize()

	// Create the status writer
	sw := &Indicator{
		statusLine: "",
		logBuffer:  make([]string, 0, calculateMinHeight(height)), // Reserve one line for status
		maxLogs:    calculateMinHeight(height),                    // Use terminal height minus status line
		resizeChan: make(chan os.Signal, 1),                       // Channel for resize events
	}

	// Set up terminal resize signal handling
	sig := SIGWINCH()
	if sig != -1 {
		signal.Notify(sw.resizeChan, sig)
		// Start a goroutine to handle terminal resize events
		go sw.handleResize()
	}

	// Hide cursor and clear screen
	fmt.Print(escapeSeqHideCursor)
	fmt.Print(escapeSeqClear)
	return sw
}

// handleResize monitors for terminal resize events and updates maxLogs
func (sw *Indicator) handleResize() {
	for range sw.resizeChan {
		_, height := getTerminalSize()

		sw.mu.Lock()
		sw.maxLogs = calculateMinHeight(height) // Update maximum log lines

		// Trim buffer if needed after resize
		if len(sw.logBuffer) > sw.maxLogs {
			sw.logBuffer = sw.logBuffer[len(sw.logBuffer)-sw.maxLogs:]
		}

		// Re-render with new dimensions
		sw.render()
		sw.mu.Unlock()
	}
}

// getTerminalSize returns the width and height of the terminal
func getTerminalSize() (width, height int) {
	width, height, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil {
		// Default fallback values if we can't detect
		return 80, 24
	}
	return width, height
}

// Write implements io.Writer to capture log output
func (sw *Indicator) Write(p []byte) (n int, err error) {
	sw.mu.Lock()
	defer sw.mu.Unlock()

	// Get the full length to return
	n = len(p)

	// Convert to string and remove trailing newlines
	logLine := bytes.TrimSuffix(p, ansiNewlineByte)

	// Split multi-line log entries
	for _, line := range bytes.Split(logLine, ansiNewlineByte) {
		// Add to log buffer
		sw.logBuffer = append(sw.logBuffer, string(line))
	}

	// Keep buffer within max size
	if len(sw.logBuffer) > sw.maxLogs {
		sw.logBuffer = sw.logBuffer[len(sw.logBuffer)-sw.maxLogs:]
	}

	// Refresh display
	sw.render()
	return n, nil
}

// UpdateStatus updates the status line and refreshes the display
func (sw *Indicator) UpdateStatus(message string) {
	sw.mu.Lock()
	defer sw.mu.Unlock()

	if sw.statusLine == message {
		return
	}

	sw.statusLine = message
	sw.render()
}

// Close restores the original logger output and cleans up
func (sw *Indicator) Close() error {
	sw.mu.Lock()
	defer sw.mu.Unlock()

	// Stop listening for resize events
	signal.Stop(sw.resizeChan)
	close(sw.resizeChan)

	// Clear the screen and reset cursor position
	fmt.Print(escapeSeqClear)
	fmt.Print(escapeSeqShowCursor)

	return nil
}

// wrapLine wraps a line to fit within the given width
func wrapLine(line string, width int) []string {
	if width <= 0 {
		return []string{line}
	}

	if len(line) <= width {
		return []string{line}
	}

	var wrapped []string
	for len(line) > width {
		wrapped = append(wrapped, line[:width])
		line = line[width:]
	}
	if len(line) > 0 {
		wrapped = append(wrapped, line)
	}
	return wrapped
}

// render redraws the entire visible area with logs and status
func (sw *Indicator) render() {
	// Get terminal dimensions
	width, _ := getTerminalSize()

	// Clear the screen
	fmt.Print(escapeSeqClear)

	// Print log buffer
	maxToShow := min(len(sw.logBuffer), sw.maxLogs)
	startIdx := len(sw.logBuffer) - maxToShow
	linesUsed := 0

	for i := 0; i < maxToShow && linesUsed < sw.maxLogs; i++ {
		logLine := sw.logBuffer[startIdx+i]
		wrappedLines := wrapLine(logLine, width)

		for _, line := range wrappedLines {
			if linesUsed >= sw.maxLogs {
				break
			}
			fmt.Println(line)
			linesUsed++
		}
	}

	maxToShow = linesUsed

	// Print empty lines to fill the space
	for i := maxToShow; i < sw.maxLogs+StatusMarginTopLines; i++ {
		fmt.Println()
	}

	// Print status line at the bottom
	fmt.Print(escapeSeqClearLine) // Clear line
	fmt.Print(ansiCarriageReturn) // Move to beginning of line

	// Handle status line with existing newlines and wrapping
	statusParts := bytes.Split([]byte(sw.statusLine), []byte("\n"))
	for partIdx, part := range statusParts {
		if partIdx > 0 {
			fmt.Print("\n")
		}

		// Wrap each part if it's too long
		wrappedLines := wrapLine(string(part), width)
		for lineIdx, line := range wrappedLines {
			if lineIdx > 0 {
				fmt.Print("\n")
			}
			fmt.Print(line)
		}
	}
}
