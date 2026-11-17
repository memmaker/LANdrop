// Screen capture. One code path for macOS and Windows via kbinani/screenshot
// (CoreGraphics on macOS, GDI BitBlt syscalls on Windows). On macOS the app
// needs the Screen Recording permission, or the capture comes back black.
package main

import (
	"bytes"
	"fmt"
	"image/png"

	"github.com/kbinani/screenshot"
)

func captureScreen(display int) ([]byte, error) {
	n := screenshot.NumActiveDisplays()
	if n == 0 {
		return nil, fmt.Errorf("no active display")
	}
	if display < 0 || display >= n {
		display = 0
	}
	img, err := screenshot.CaptureDisplay(display)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
