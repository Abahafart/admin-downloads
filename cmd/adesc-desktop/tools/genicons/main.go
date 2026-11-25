//go:build ignore

// genicons draws the AdminDescargas icons (a download arrow on a rounded
// square) without external tools:
//
//	go run ./tools/genicons
//
// It writes build/appicon.png (1024 px, Wails derives the .ico/.icns from
// it), icons/tray.png (64 px, Linux/macOS tray) and icons/tray.ico (16, 24,
// 32 and 48 px, Windows tray, classic BMP entries).
package main

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"log"
	"math"
	"os"
)

var (
	accent = color.NRGBA{0x1f, 0x5f, 0xd1, 0xff} // Grafito light accent
	ink    = color.NRGBA{0xff, 0xff, 0xff, 0xff}
)

func roundedRect(x, y, x0, y0, x1, y1, r float64) bool {
	if x < x0 || x > x1 || y < y0 || y > y1 {
		return false
	}
	cx := math.Max(x0+r, math.Min(x, x1-r))
	cy := math.Max(y0+r, math.Min(y, y1-r))
	return (x-cx)*(x-cx)+(y-cy)*(y-cy) <= r*r
}

func inTriangle(px, py, ax, ay, bx, by, cx, cy float64) bool {
	s := func(x1, y1, x2, y2, x3, y3 float64) float64 { return (x1-x3)*(y2-y3) - (x2-x3)*(y1-y3) }
	d1, d2, d3 := s(px, py, ax, ay, bx, by), s(px, py, bx, by, cx, cy), s(px, py, cx, cy, ax, ay)
	neg := d1 < 0 || d2 < 0 || d3 < 0
	pos := d1 > 0 || d2 > 0 || d3 > 0
	return !(neg && pos)
}

// shade returns the color of the unit-square point (x, y).
func shade(x, y float64) (color.NRGBA, bool) {
	if !roundedRect(x, y, 0.06, 0.06, 0.94, 0.94, 0.22) {
		return color.NRGBA{}, false
	}
	arrow := roundedRect(x, y, 0.435, 0.18, 0.565, 0.56, 0.03) ||
		inTriangle(x, y, 0.25, 0.47, 0.75, 0.47, 0.50, 0.73) ||
		roundedRect(x, y, 0.25, 0.78, 0.75, 0.87, 0.045)
	if arrow {
		return ink, true
	}
	return accent, true
}

func draw(size int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	const ss = 4
	for py := 0; py < size; py++ {
		for px := 0; px < size; px++ {
			var r, g, b, a float64
			for sy := 0; sy < ss; sy++ {
				for sx := 0; sx < ss; sx++ {
					x := (float64(px) + (float64(sx)+0.5)/ss) / float64(size)
					y := (float64(py) + (float64(sy)+0.5)/ss) / float64(size)
					if c, ok := shade(x, y); ok {
						r += float64(c.R)
						g += float64(c.G)
						b += float64(c.B)
						a++
					}
				}
			}
			if a == 0 {
				continue
			}
			img.SetNRGBA(px, py, color.NRGBA{uint8(r / a), uint8(g / a), uint8(b / a), uint8(255 * a / (ss * ss))})
		}
	}
	return img
}

func writePNG(path string, img image.Image) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		log.Fatal(err)
	}
}

// bmpEntry encodes img as an ICO image entry: BITMAPINFOHEADER (double
// height), 32-bit BGRA rows bottom-up, then the 1-bit AND mask.
func bmpEntry(img *image.NRGBA) []byte {
	n := img.Rect.Dx()
	var buf bytes.Buffer
	hdr := struct {
		Size                   uint32
		Width, Height          int32
		Planes, BitCount       uint16
		Compression, SizeImage uint32
		XPPM, YPPM             int32
		ClrUsed, ClrImportant  uint32
	}{40, int32(n), int32(2 * n), 1, 32, 0, 0, 0, 0, 0, 0}
	_ = binary.Write(&buf, binary.LittleEndian, hdr)
	for y := n - 1; y >= 0; y-- {
		for x := 0; x < n; x++ {
			c := img.NRGBAAt(x, y)
			buf.Write([]byte{c.B, c.G, c.R, c.A})
		}
	}
	stride := ((n + 31) / 32) * 4
	mask := make([]byte, stride*n) // all zero: alpha channel decides
	buf.Write(mask)
	return buf.Bytes()
}

func writeICO(path string, sizes []int) {
	var entries [][]byte
	for _, s := range sizes {
		entries = append(entries, bmpEntry(draw(s)))
	}
	var out bytes.Buffer
	_ = binary.Write(&out, binary.LittleEndian, [3]uint16{0, 1, uint16(len(sizes))})
	offset := 6 + 16*len(sizes)
	for i, s := range sizes {
		dim := uint8(s)
		if s >= 256 {
			dim = 0
		}
		_ = binary.Write(&out, binary.LittleEndian, struct {
			W, H, Colors, Reserved uint8
			Planes, BitCount       uint16
			Size, Offset           uint32
		}{dim, dim, 0, 0, 1, 32, uint32(len(entries[i])), uint32(offset)})
		offset += len(entries[i])
	}
	for _, e := range entries {
		out.Write(e)
	}
	if err := os.WriteFile(path, out.Bytes(), 0o644); err != nil {
		log.Fatal(err)
	}
}

func main() {
	writePNG("build/appicon.png", draw(1024))
	writePNG("icons/tray.png", draw(64))
	writeICO("icons/tray.ico", []int{16, 24, 32, 48})
}
