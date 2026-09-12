// Package pngprofile applies deterministic bounded palette and geometry
// profiles to an already-rendered PNG without knowing terminal or transport.
package pngprofile

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"sort"

	xdraw "golang.org/x/image/draw"
)

const profilePaletteSize = 8

var ErrInvalidProfile = errors.New("invalid PNG image profile")

type Profile uint8

const (
	ProfileCurrent Profile = iota
	ProfileFull8
	ProfileCompact8
)

func (profile Profile) Valid() bool {
	return profile == ProfileCurrent || profile == ProfileFull8 || profile == ProfileCompact8
}

func Apply(data []byte, profile Profile) ([]byte, error) {
	if profile == ProfileCurrent {
		return data, nil
	}
	if !profile.Valid() {
		return nil, ErrInvalidProfile
	}
	decoded, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	if profile == ProfileCompact8 {
		bounds := decoded.Bounds()
		width := max(1, bounds.Dx()*3/4)
		height := max(1, bounds.Dy()*3/4)
		resized := image.NewRGBA(image.Rect(0, 0, width, height))
		xdraw.CatmullRom.Scale(resized, resized.Bounds(), decoded, bounds, xdraw.Over, nil)
		decoded = resized
	}
	paletted := quantize(decoded, profilePaletteSize)
	var output bytes.Buffer
	encoder := png.Encoder{CompressionLevel: png.BestCompression}
	if err := encoder.Encode(&output, paletted); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

type colorCount struct {
	r, g, b uint8
	count   uint64
}

type colorBox struct {
	colors []colorCount
	rangeR uint8
	rangeG uint8
	rangeB uint8
	count  uint64
}

func newColorBox(colors []colorCount) colorBox {
	box := colorBox{colors: colors}
	if len(colors) == 0 {
		return box
	}
	minR, maxR := colors[0].r, colors[0].r
	minG, maxG := colors[0].g, colors[0].g
	minB, maxB := colors[0].b, colors[0].b
	for _, candidate := range colors {
		minR, maxR = min(minR, candidate.r), max(maxR, candidate.r)
		minG, maxG = min(minG, candidate.g), max(maxG, candidate.g)
		minB, maxB = min(minB, candidate.b), max(maxB, candidate.b)
		box.count += candidate.count
	}
	box.rangeR = maxR - minR
	box.rangeG = maxG - minG
	box.rangeB = maxB - minB
	return box
}

func quantize(source image.Image, maxColors int) *image.Paletted {
	rgba := rgbaImage(source)
	bounds := rgba.Bounds()
	histogram := make(map[uint32]uint64)
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			pixel := rgba.RGBAAt(x, y)
			key := uint32(pixel.R)<<16 | uint32(pixel.G)<<8 | uint32(pixel.B)
			histogram[key]++
		}
	}
	colors := make([]colorCount, 0, len(histogram))
	for key, count := range histogram {
		colors = append(colors, colorCount{r: uint8(key >> 16), g: uint8(key >> 8), b: uint8(key), count: count})
	}
	sort.Slice(colors, func(i, j int) bool { return rgbLess(colors[i], colors[j]) })
	boxes := []colorBox{newColorBox(colors)}
	for len(boxes) < maxColors {
		index := boxToSplit(boxes)
		if index < 0 {
			break
		}
		left, right := splitBox(boxes[index])
		boxes[index] = left
		boxes = append(boxes, right)
	}
	paletteColors := make([]color.RGBA, 0, len(boxes))
	palette := make(color.Palette, 0, len(boxes))
	for _, box := range boxes {
		average := boxAverage(box)
		paletteColors = append(paletteColors, average)
		palette = append(palette, average)
	}
	destination := image.NewPaletted(image.Rect(0, 0, bounds.Dx(), bounds.Dy()), palette)
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			pixel := rgba.RGBAAt(x, y)
			destination.SetColorIndex(x-bounds.Min.X, y-bounds.Min.Y, nearestPaletteIndex(paletteColors, pixel.R, pixel.G, pixel.B))
		}
	}
	return destination
}

func rgbaImage(source image.Image) *image.RGBA {
	if rgba, ok := source.(*image.RGBA); ok {
		return rgba
	}
	bounds := source.Bounds()
	rgba := image.NewRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	draw.Draw(rgba, rgba.Bounds(), source, bounds.Min, draw.Src)
	return rgba
}

func boxToSplit(boxes []colorBox) int {
	selected := -1
	var selectedScore uint64
	for index, box := range boxes {
		if len(box.colors) < 2 {
			continue
		}
		rangeMax := max(box.rangeR, max(box.rangeG, box.rangeB))
		score := uint64(rangeMax) * box.count
		if selected < 0 || score > selectedScore {
			selected, selectedScore = index, score
		}
	}
	return selected
}

func splitBox(box colorBox) (colorBox, colorBox) {
	channel := 0
	if box.rangeG > box.rangeR {
		channel = 1
	}
	if box.rangeB > max(box.rangeR, box.rangeG) {
		channel = 2
	}
	sort.Slice(box.colors, func(i, j int) bool {
		left, right := box.colors[i], box.colors[j]
		leftPrimary := colorChannel(left, channel)
		rightPrimary := colorChannel(right, channel)
		if leftPrimary != rightPrimary {
			return leftPrimary < rightPrimary
		}
		return rgbLess(left, right)
	})
	half := (box.count + 1) / 2
	var count uint64
	split := 1
	for split < len(box.colors)-1 {
		count += box.colors[split-1].count
		if count >= half {
			break
		}
		split++
	}
	return newColorBox(box.colors[:split]), newColorBox(box.colors[split:])
}

func colorChannel(candidate colorCount, channel int) uint8 {
	switch channel {
	case 1:
		return candidate.g
	case 2:
		return candidate.b
	default:
		return candidate.r
	}
}

func rgbLess(left, right colorCount) bool {
	if left.r != right.r {
		return left.r < right.r
	}
	if left.g != right.g {
		return left.g < right.g
	}
	return left.b < right.b
}

func boxAverage(box colorBox) color.RGBA {
	var red, green, blue uint64
	for _, candidate := range box.colors {
		red += uint64(candidate.r) * candidate.count
		green += uint64(candidate.g) * candidate.count
		blue += uint64(candidate.b) * candidate.count
	}
	return color.RGBA{uint8(red / box.count), uint8(green / box.count), uint8(blue / box.count), 255}
}

func nearestPaletteIndex(palette []color.RGBA, red, green, blue uint8) uint8 {
	bestIndex := 0
	bestDistance := int(^uint(0) >> 1)
	for index, candidate := range palette {
		deltaR := int(red) - int(candidate.R)
		deltaG := int(green) - int(candidate.G)
		deltaB := int(blue) - int(candidate.B)
		distance := 3*deltaR*deltaR + 6*deltaG*deltaG + deltaB*deltaB
		if distance < bestDistance {
			bestIndex, bestDistance = index, distance
		}
	}
	return uint8(bestIndex)
}
