package testharness

import (
	"image"
	"image/png"
	"os"
)

// savePNG writes an *image.RGBA to disk as a PNG file. Used by
// SaveScreenshot for human debugging when an automated test fails.
func savePNG(path string, img *image.RGBA) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	return png.Encode(f, img)
}
