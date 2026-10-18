//go:build ignore

// genicon draws the kube-dump icon and writes it as assets/icon.ico (Windows, PNG-compressed
// entries, 16-256 px) and as PNG files in the freedesktop hicolor layout under assets/icons
// (Linux desktop entries). Run from the repository root: go run assets/genicon.go
package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"log"
	"os"
	"path/filepath"
)

const ss = 4 // supersampling per axis

// roundRect reports whether (x, y) is inside the rounded rectangle.
func roundRect(x, y, x0, y0, x1, y1, r float64) bool {
	if x < x0 || x > x1 || y < y0 || y > y1 {
		return false
	}
	cx := clamp(x, x0+r, x1-r)
	cy := clamp(y, y0+r, y1-r)
	dx, dy := x-cx, y-cy
	return dx*dx+dy*dy <= r*r
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func mix(a, b color.RGBA, t float64) color.RGBA {
	f := func(x, y uint8) uint8 { return uint8(float64(x)*(1-t) + float64(y)*t) }
	return color.RGBA{f(a.R, b.R), f(a.G, b.G), f(a.B, b.B), 255}
}

// pixel returns the color at (u, v) in the unit square, or alpha 0 outside the icon.
func pixel(u, v float64) color.RGBA {
	if !roundRect(u, v, 0.03, 0.03, 0.97, 0.97, 0.22) {
		return color.RGBA{}
	}
	// background: deep blue, lighter at the top
	c := mix(color.RGBA{0x2F, 0x55, 0x8F, 255}, color.RGBA{0x12, 0x1E, 0x38, 255}, v)

	white := color.RGBA{0xF4, 0xF7, 0xFB, 255}
	orange := color.RGBA{0xFF, 0x8A, 0x1F, 255}
	// a call stack: bars of different length, one hot thread in orange
	bars := []struct {
		x0, x1 float64
		col    color.RGBA
	}{
		{0.20, 0.74, white},
		{0.20, 0.60, white},
		{0.20, 0.82, orange},
		{0.20, 0.52, white},
	}
	const top, h, gap = 0.215, 0.115, 0.058
	for i, b := range bars {
		y0 := top + float64(i)*(h+gap)
		if roundRect(u, v, b.x0, y0, b.x1, y0+h, h/2) {
			return b.col
		}
	}
	return c
}

func render(size int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	for py := 0; py < size; py++ {
		for px := 0; px < size; px++ {
			var r, g, b, a float64
			for sy := 0; sy < ss; sy++ {
				for sx := 0; sx < ss; sx++ {
					u := (float64(px) + (float64(sx)+0.5)/ss) / float64(size)
					v := (float64(py) + (float64(sy)+0.5)/ss) / float64(size)
					c := pixel(u, v)
					if c.A == 0 {
						continue
					}
					r += float64(c.R)
					g += float64(c.G)
					b += float64(c.B)
					a++
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

func main() {
	sizes := []int{16, 24, 32, 48, 64, 128, 256}
	var pngs [][]byte
	for _, s := range sizes {
		var buf bytes.Buffer
		if err := png.Encode(&buf, render(s)); err != nil {
			log.Fatal(err)
		}
		pngs = append(pngs, buf.Bytes())
	}

	var out bytes.Buffer
	binary.Write(&out, binary.LittleEndian, [3]uint16{0, 1, uint16(len(sizes))}) // reserved, type=icon, count
	offset := 6 + 16*len(sizes)
	for i, s := range sizes {
		dim := byte(s)
		if s == 256 {
			dim = 0 // 0 means 256
		}
		out.Write([]byte{dim, dim, 0, 0})
		binary.Write(&out, binary.LittleEndian, [2]uint16{1, 32})                                // planes, bits per pixel
		binary.Write(&out, binary.LittleEndian, [2]uint32{uint32(len(pngs[i])), uint32(offset)}) // size, offset
		offset += len(pngs[i])
	}
	for _, p := range pngs {
		out.Write(p)
	}
	if err := os.WriteFile("assets/icon.ico", out.Bytes(), 0o644); err != nil {
		log.Fatal(err)
	}
	for i, sz := range sizes {
		dir := filepath.Join("assets", "icons", "hicolor", fmt.Sprintf("%dx%d", sz, sz), "apps")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			log.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "kube-dump.png"), pngs[i], 0o644); err != nil {
			log.Fatal(err)
		}
	}
	log.Printf("assets/icon.ico: %d bytes, sizes %v; PNGs in assets/icons/hicolor", out.Len(), sizes)
}
