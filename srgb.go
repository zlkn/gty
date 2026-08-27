package main

// Colours are sRGB on the Go side — that is what the config writes and what OSC 10/11 must
// answer with — while wgpu takes a fragment's output and a clear value as linear light and
// encodes on write to an *UnormSrgb target. Untouched they are encoded twice and land far
// too light: #424242 comes out #8b8b8b. So the decode happens as late as possible: in
// vs_main in both shaders, and here for the clear value, which no shader sees.

import (
	"math"

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

// pickFormat keeps the driver's order except that an sRGB variant wins: it is what a
// compositor wants, and it puts blending in linear light.
func pickFormat(formats []wgpu.TextureFormat) wgpu.TextureFormat {
	for _, f := range formats {
		if isSrgbFormat(f) {
			return f
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

// darkOnLightGamma bends the coverage curve when ink is darker than paper: linear blending
// puts a half-covered pixel nearer the paper than the eye expects, which reads thin. Only
// the edges move — a full core is a fixed point of any exponent.
//
// Past the usual 1.5-2 because this build draws Light and hints nothing: at 17pt, 16 of 94
// ASCII glyphs never reach full coverage ('_' peaks at 58%). Past 2.6 it reads as bold.
const darkOnLightGamma = 2.2

// coverageExponent is what the shader raises coverage to before it becomes alpha; 1 leaves
// the rasteriser's own. gamma overrides the theme when positive, clamped because pow with a
// zero or negative exponent is worse than a wrong-looking screen.
func coverageExponent(fg, bg [4]float32, gamma float64) float32 {
	if gamma <= 0 {
		gamma = 1 // light on dark already reads heavy enough
		if relLuminance(bg) > relLuminance(fg) {
			gamma = darkOnLightGamma
		}
	}
	return float32(1 / min(max(gamma, 0.25), 4))
}
