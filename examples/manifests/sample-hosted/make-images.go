// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

//go:build ignore

// make-images draws the example's cover and page files (plain panels with a page number), so
// that the example carries no third-party art. Run it from this folder:
//
//	go run make-images.go
package main

import (
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"
	"log"
	"os"
	"path/filepath"
)

var (
	night = color.RGBA{0x1d, 0x2b, 0x53, 0xff}
	paper = color.RGBA{0xf4, 0xef, 0xe1, 0xff}
	ink   = color.RGBA{0x22, 0x22, 0x22, 0xff}
	lamp  = color.RGBA{0xff, 0xc8, 0x57, 0xff}
)

// digits are 3×5 block glyphs.
var digits = map[rune][5]string{
	'0': {"###", "#.#", "#.#", "#.#", "###"}, '1': {".#.", "##.", ".#.", ".#.", "###"},
	'2': {"###", "..#", "###", "#..", "###"}, '3': {"###", "..#", "###", "..#", "###"},
	'4': {"#.#", "#.#", "###", "..#", "..#"}, '5': {"###", "#..", "###", "..#", "###"},
	'6': {"###", "#..", "###", "#.#", "###"}, '7': {"###", "..#", "..#", "..#", "..#"},
	'8': {"###", "#.#", "###", "#.#", "###"}, '9': {"###", "#.#", "###", "..#", "###"},
}

func fill(img draw.Image, r image.Rectangle, c color.Color) {
	draw.Draw(img, r, &image.Uniform{c}, image.Point{}, draw.Src)
}

func label(img draw.Image, text string, x, y, cell int, c color.Color) {
	for _, ch := range text {
		for row, line := range digits[ch] {
			for col, on := range line {
				if on == '#' {
					fill(img, image.Rect(x+col*cell, y+row*cell, x+(col+1)*cell, y+(row+1)*cell), c)
				}
			}
		}
		x += 4 * cell
	}
}

// page draws panels of panelHeight down a w×h page, with the number in the first one.
func page(w, h, panelHeight int, number string, bg color.Color) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	fill(img, img.Bounds(), bg)
	for y := 40; y+120 < h; y += panelHeight {
		bottom := min(y+panelHeight-40, h-40)
		fill(img, image.Rect(40, y, w-40, bottom), ink)
		fill(img, image.Rect(48, y+8, w-48, bottom-8), paper)
		fill(img, image.Rect(w/2-30, y+60, w/2+30, y+120), lamp)
	}
	label(img, number, 80, 80, 24, ink)
	return img
}

func save(path string, img image.Image) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		log.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()
	if filepath.Ext(path) == ".jpg" {
		err = jpeg.Encode(f, img, &jpeg.Options{Quality: 85})
	} else {
		err = png.Encode(f, img)
	}
	if err != nil {
		log.Fatal(err)
	}
}

func main() {
	cover := page(400, 600, 600, "0", night)
	save("covers/night-ferry.png", cover)
	save("ch1/01.png", page(800, 1200, 600, "1", night))
	save("ch1/02.png", page(800, 1200, 400, "2", night))
	// A long strip taller than 6000 px: the import cuts it into two slices of 4500 px.
	save("ch2/strip.png", page(800, 9000, 750, "3", night))
	save("ch3/01.jpg", page(800, 1200, 1200, "4", night))
}
