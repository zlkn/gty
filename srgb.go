package main

// Colours are sRGB on the Go side — that is what the config writes and what OSC 10/11 must
// answer with — while wgpu takes a fragment's output and a clear value as linear light and
// encodes on write to an *UnormSrgb target. Untouched they are encoded twice and land far
// too light: #424242 comes out #8b8b8b. So the decode happens as late as possible: in
// vs_main in both shaders, and here for the clear value, which no shader sees.

import (
	"maps"
	"math"
	"slices"
	"strings"

	"github.com/oliverbestmann/webgpu/wgpu"
)

// isSrgbFormat reports whether the target encodes on write. Those two are the only sRGB
// formats a surface can be configured with.
func isSrgbFormat(f wgpu.TextureFormat) bool {
	switch f {
	case wgpu.TextureFormatRGBA8UnormSrgb, wgpu.TextureFormatBGRA8UnormSrgb:
		return true
	}
	return false
}

// isGammaFormat reports whether the target takes what is written to it unchanged, so
// that blending happens in the sRGB numbers.
//
// A list rather than "not sRGB": a surface also offers RGBA16Unorm, RGB10A2Unorm and
// the like, which need device features this build does not enable. Choosing one of those
// fails pipeline creation outright, which is a black window rather than a wrong colour.
func isGammaFormat(f wgpu.TextureFormat) bool {
	switch f {
	case wgpu.TextureFormatRGBA8Unorm, wgpu.TextureFormatBGRA8Unorm:
		return true
	}
	return false
}

// blendSpace is where the GPU mixes a glyph's edge with what is behind it. The surface
// format decides it: an sRGB target is blended in linear light, a plain UNORM one in the
// numbers as written.
type blendSpace uint8

const (
	// blendGamma mixes the sRGB numbers. Not physically right, and what every terminal
	// does: linear light drags a half-covered pixel towards grey, so #007474 antialiases
	// to #adbcbc where this gives #76b0b0.
	blendGamma blendSpace = iota
	blendLinear
)

var blendNames = map[string]blendSpace{
	"gamma":  blendGamma,
	"linear": blendLinear,
}

func blendList() string {
	return strings.Join(slices.Sorted(maps.Keys(blendNames)), " ")
}

func (b blendSpace) String() string {
	if b == blendLinear {
		return "linear"
	}
	return "gamma"
}

// spaceOf is the blending a format imposes. Asked of the format already chosen, so the
// answer is never about one this renderer would refuse.
func spaceOf(f wgpu.TextureFormat) blendSpace {
	if isSrgbFormat(f) {
		return blendLinear
	}
	return blendGamma
}

// pickFormat keeps the driver's order and takes the first format blending in want. The
// other known kind is the fallback before the driver's own lead, so a device offering
// neither still gets something this renderer can draw into; the caller reads the space
// back off the result either way.
func pickFormat(formats []wgpu.TextureFormat, want blendSpace) wgpu.TextureFormat {
	first, second := isGammaFormat, isSrgbFormat
	if want == blendLinear {
		first, second = isSrgbFormat, isGammaFormat
	}
	for _, fits := range []func(wgpu.TextureFormat) bool{first, second} {
		for _, f := range formats {
			if fits(f) {
				return f
			}
		}
	}
	if len(formats) == 0 {
		return wgpu.TextureFormatUndefined
	}
	return formats[0]
}

// srgbToLinear is the inverse transfer function, piecewise rather than the 2.2-gamma
// approximation: the straight segment below 0.04045 is where a light theme's ink lives.
// Both shaders carry the same curve.
func srgbToLinear(v float64) float64 {
	if v <= 0.04045 {
		return v / 12.92
	}
	return math.Pow((v+0.055)/1.055, 2.4)
}

// clearValue is c decoded when the target will encode. Alpha is coverage, not colour, so
// no transfer function applies to it.
func clearValue(c [4]float32, srgbTarget bool) wgpu.Color {
	out := wgpu.Color{R: float64(c[0]), G: float64(c[1]), B: float64(c[2]), A: float64(c[3])}
	if srgbTarget {
		out.R, out.G, out.B = srgbToLinear(out.R), srgbToLinear(out.G), srgbToLinear(out.B)
	}
	return out
}

// srgbFlag is what the viewport uniform carries to the shaders, which have no bool.
func srgbFlag(srgbTarget bool) float32 {
	if srgbTarget {
		return 1
	}
	return 0
}

// relLuminance decodes first: a mean of sRGB numbers is not a brightness.
func relLuminance(c [4]float32) float64 {
	r, g, b := srgbToLinear(float64(c[0])), srgbToLinear(float64(c[1])), srgbToLinear(float64(c[2]))
	return 0.2126*r + 0.7152*g + 0.0722*b
}

// darkOnLightGamma bends the coverage curve for dark ink on light paper blended in linear
// light, which puts a half-covered pixel nearer the paper than the eye expects and reads
// thin. Only the edges move — a full core is a fixed point of any exponent. Gamma-space
// blending needs none of this; it is already heavy enough there.
//
// Past the usual 1.5-2 because this build draws Light: hinting fits horizontal edges only,
// so at 17pt 10 of 94 ASCII glyphs still peak short of full coverage. Past 2.6 reads bold.
const darkOnLightGamma = 2.2

// coverageExponent is what the shader raises coverage to before it becomes alpha; 1 leaves
// the rasteriser's own. gamma overrides the derivation when positive, clamped because pow
// with a zero or negative exponent is worse than a wrong-looking screen.
func coverageExponent(fg, bg [4]float32, gamma float64, space blendSpace) float32 {
	if gamma <= 0 {
		gamma = 1 // gamma-space blending, and light on dark, already read heavy enough
		if space == blendLinear && relLuminance(bg) > relLuminance(fg) {
			gamma = darkOnLightGamma
		}
	}
	return float32(1 / min(max(gamma, 0.25), 4))
}
