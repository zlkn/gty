package main

// Colours are sRGB everywhere on the Go side: that is what the config file writes, what
// a program sends over the PTY, and what OSC 10/11 has to answer with. The GPU is the
// other end of that wire — wgpu takes a fragment's output and a clear value as linear
// light, and a target in an *UnormSrgb format applies the sRGB encode on the way to
// memory. A colour handed over untouched is therefore encoded twice and lands far too
// light: #424242 comes out #8b8b8b, #f2f2f2 comes out #f9f9f9, the theme washed out.
//
// So the decode happens as late as it can, and nothing upstream has to think in linear
// light: in vs_main in text.wgsl and rect.wgsl for everything drawn as a quad, and here
// for the clear value — the one colour that never passes through a shader.

import (
	"math"

	"github.com/oliverbestmann/webgpu/wgpu"
)

// isSrgbFormat reports whether the target applies the sRGB encode on write.
//
// Those two are the only sRGB formats a surface can be configured with, so everything
// else is a target that stores what the shader wrote — which is what the offscreen tests
// render to when they want to read a colour back unchanged.
func isSrgbFormat(f wgpu.TextureFormat) bool {
	switch f {
	case wgpu.TextureFormatRGBA8UnormSrgb, wgpu.TextureFormatBGRA8UnormSrgb:
		return true
	}
	return false
}

// pickFormat is the surface format to configure, out of what the driver offers.
//
// The driver's own order is kept, except that an sRGB variant wins outright: it is the
// format a compositor wants, and it puts alpha blending in linear light, which is correct
// rather than merely conventional. Which of the two it is stops mattering after that,
// because the decode is keyed off this same value — see isSrgbFormat.
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

// srgbToLinear is the inverse sRGB transfer function, one channel at a time.
//
// The piecewise form, not the 2.2-gamma approximation: the straight segment below 0.04045
// is where a light theme's ink lives, and it is the part the approximation gets wrong.
// text.wgsl and rect.wgsl carry the same curve for the colours that reach the GPU as
// vertex data; this copy is for the one that does not.
func srgbToLinear(v float64) float64 {
	if v <= 0.04045 {
		return v / 12.92
	}
	return math.Pow((v+0.055)/1.055, 2.4)
}

// clearValue is c as the render pass wants it, decoded when the target will encode.
//
// Alpha is passed through: it is a fraction of coverage, not a colour, and no transfer
// function applies to it.
func clearValue(c [4]float32, srgbTarget bool) wgpu.Color {
	out := wgpu.Color{R: float64(c[0]), G: float64(c[1]), B: float64(c[2]), A: float64(c[3])}
	if srgbTarget {
		out.R, out.G, out.B = srgbToLinear(out.R), srgbToLinear(out.G), srgbToLinear(out.B)
	}
	return out
}

// srgbFlag is what the viewport uniform carries to the shaders, which have no bool: 1
// where they have to decode, 0 where the target stores what they wrote.
func srgbFlag(srgbTarget bool) float32 {
	if srgbTarget {
		return 1
	}
	return 0
}

// Coverage is the other half of getting linear blending to look right.
//
// Blending in linear light is correct, and it makes dark ink on light paper read thin:
// at half coverage the linear average sits much closer to the paper than the eye expects
// a half-covered pixel to, so the halo around a stem lightens and the stroke narrows to
// its fully covered core. Bending the coverage curve puts the weight back — and only at
// the edges, because a core of coverage 1 is a fixed point of any exponent.
//
// Which direction to bend depends on the theme: light ink on a dark background gains
// weight from linear blending rather than losing it, so there it is left alone.

// relLuminance is the CIE relative luminance of a colour the config file wrote, so it has
// to be decoded first: a mean of sRGB numbers is not a brightness.
func relLuminance(c [4]float32) float64 {
	r, g, b := srgbToLinear(float64(c[0])), srgbToLinear(float64(c[1])), srgbToLinear(float64(c[2]))
	return 0.2126*r + 0.7152*g + 0.0722*b
}

// darkOnLightGamma is how much the coverage curve is bent when ink is darker than paper.
// Somewhere between 1.5 and 2 is where this is usually settled, stem darkening and text
// gamma knobs elsewhere included; measured here on JetBrains Mono Light at 24pt it puts
// about a sixth of a stem's ink mass back into its edge pixels and leaves the core exactly
// where it was. The config file can say otherwise — see coverageExponent.
const darkOnLightGamma = 1.8

// coverageExponent is what the shader raises the atlas coverage to before it becomes
// alpha. Below 1 it thickens the partial pixels at the edge of a stem; 1 is the coverage
// the rasteriser produced, untouched.
//
// gamma overrides the choice when it is positive — the config file's own knob, for a
// judgement no formula settles. It is clamped rather than rejected: a wild value is a
// screen of unreadable text, and pow with a zero or negative exponent is worse than that.
func coverageExponent(fg, bg [4]float32, gamma float64) float32 {
	if gamma <= 0 {
		gamma = 1 // light on dark: linear blending already reads heavy enough
		if relLuminance(bg) > relLuminance(fg) {
			gamma = darkOnLightGamma
		}
	}
	return float32(1 / min(max(gamma, 0.25), 4))
}
