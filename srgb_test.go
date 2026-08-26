package main

import (
	"math"
	"testing"

	"github.com/oliverbestmann/webgpu/wgpu"
)

// This file is the only cover the transfer function has on a machine with no GPU: every
// test in render_test.go skips without an adapter.

func TestSrgbToLinear(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   float64
		want float64
	}{
		// Both ends are fixed points, and they have to stay that way: #000000 and
		// #ffffff must survive the round trip through the GPU's encode exactly.
		{"black", 0, 0},
		{"white", 1, 1},
		// The join between the two segments, from either side.
		{"toe", 0.04045, 0.04045 / 12.92},
		{"just above the toe", 0.04046, 0.0031308},
		// The theme's own colours, which is what the bug was measured on.
		{"background #f2f2f2", 0.949, 0.8879},
		{"foreground #424242", 0.259, 0.0546},
		{"mid grey", 0.5, 0.2140},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := srgbToLinear(tc.in); math.Abs(got-tc.want) > 1e-4 {
				t.Errorf("srgbToLinear(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// TestSrgbToLinearIsMonotonic: the curve is what stands between the config file and the
// screen, so an ordering it does not preserve would reorder the palette.
func TestSrgbToLinearIsMonotonic(t *testing.T) {
	prev := -1.0
	for i := range 256 {
		v := srgbToLinear(float64(i) / 255)
		if v <= prev {
			t.Fatalf("srgbToLinear is not increasing at byte %d: %v after %v", i, v, prev)
		}
		if v < 0 || v > 1 {
			t.Fatalf("srgbToLinear(%v) = %v, outside [0,1]", float64(i)/255, v)
		}
		prev = v
	}
}

func TestIsSrgbFormat(t *testing.T) {
	for _, f := range []wgpu.TextureFormat{
		wgpu.TextureFormatRGBA8UnormSrgb,
		wgpu.TextureFormatBGRA8UnormSrgb,
	} {
		if !isSrgbFormat(f) {
			t.Errorf("%v: want an sRGB format", f)
		}
	}
	for _, f := range []wgpu.TextureFormat{
		wgpu.TextureFormatRGBA8Unorm,
		wgpu.TextureFormatBGRA8Unorm,
		wgpu.TextureFormatR8Unorm,
		wgpu.TextureFormatUndefined,
	} {
		if isSrgbFormat(f) {
			t.Errorf("%v: want a linear format", f)
		}
	}
}

// TestPickFormat: an sRGB variant wins wherever the driver put it, and the driver's own
// order decides between two of them. Deterministic, because the decode is keyed off
// whatever this returns.
func TestPickFormat(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   []wgpu.TextureFormat
		want wgpu.TextureFormat
	}{
		{
			"sRGB first, as Mesa reports it",
			[]wgpu.TextureFormat{wgpu.TextureFormatBGRA8UnormSrgb, wgpu.TextureFormatBGRA8Unorm},
			wgpu.TextureFormatBGRA8UnormSrgb,
		},
		{
			"sRGB further down the list",
			[]wgpu.TextureFormat{wgpu.TextureFormatBGRA8Unorm, wgpu.TextureFormatBGRA8UnormSrgb},
			wgpu.TextureFormatBGRA8UnormSrgb,
		},
		{
			"two sRGB variants: the driver's order decides",
			[]wgpu.TextureFormat{wgpu.TextureFormatRGBA8UnormSrgb, wgpu.TextureFormatBGRA8UnormSrgb},
			wgpu.TextureFormatRGBA8UnormSrgb,
		},
		{
			"no sRGB on offer",
			[]wgpu.TextureFormat{wgpu.TextureFormatBGRA8Unorm, wgpu.TextureFormatRGBA8Unorm},
			wgpu.TextureFormatBGRA8Unorm,
		},
		{"nothing on offer", nil, wgpu.TextureFormatUndefined},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := pickFormat(tc.in); got != tc.want {
				t.Errorf("pickFormat(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// TestClearValueDecodesOnlyForSrgbTargets: the clear is the one colour no shader touches,
// so this is the whole of its correctness.
func TestClearValueDecodesOnlyForSrgbTargets(t *testing.T) {
	c := [4]float32{0.949, 0.259, 0, 0.5}

	linear := clearValue(c, false)
	if linear.R != float64(c[0]) || linear.G != float64(c[1]) || linear.B != float64(c[2]) {
		t.Errorf("a linear target got %v, want the colour verbatim %v", linear, c)
	}

	encoded := clearValue(c, true)
	if encoded.R >= linear.R || encoded.G >= linear.G {
		t.Errorf("an sRGB target got %v, want every channel below the sRGB value %v", encoded, linear)
	}
	if encoded.B != 0 {
		t.Errorf("black came back as %v, want 0 — the curve's fixed point moved", encoded.B)
	}
	// Alpha is coverage, not colour: no transfer function applies to it.
	if encoded.A != 0.5 || linear.A != 0.5 {
		t.Errorf("alpha is %v and %v, want 0.5 either way", encoded.A, linear.A)
	}
}

func TestSrgbFlag(t *testing.T) {
	if srgbFlag(true) != 1 || srgbFlag(false) != 0 {
		t.Errorf("srgbFlag: got %v and %v, want 1 and 0", srgbFlag(true), srgbFlag(false))
	}
}

// TestCoverageExponent: which way the coverage curve bends is a property of the theme,
// and getting the direction wrong would thin the very text it is meant to thicken.
func TestCoverageExponent(t *testing.T) {
	light := [4]float32{0.949, 0.949, 0.949, 1} // #f2f2f2
	dark := [4]float32{0.259, 0.259, 0.259, 1}  // #424242

	if got, want := coverageExponent(dark, light, 0), float32(1/darkOnLightGamma); got != want {
		t.Errorf("dark ink on light paper: exponent %v, want %v — the edges need thickening", got, want)
	}
	if got := coverageExponent(light, dark, 0); got != 1 {
		t.Errorf("light ink on dark paper: exponent %v, want 1 — linear blending already reads heavy", got)
	}

	// The knob wins over the theme, in either direction.
	if got, want := coverageExponent(dark, light, 2), float32(0.5); got != want {
		t.Errorf("gamma 2 gave %v, want %v", got, want)
	}
	if got, want := coverageExponent(light, dark, 1.25), float32(1/1.25); got != want {
		t.Errorf("gamma 1.25 gave %v, want %v", got, want)
	}

	// Clamped, never zero or negative: pow with either would be a screen of NaN.
	for _, gamma := range []float64{-3, 0.01, 1e9} {
		if got := coverageExponent(dark, light, gamma); got <= 0 || got > 4 {
			t.Errorf("gamma %v gave exponent %v, want something a pow can use", gamma, got)
		}
	}
}

// TestRelLuminance: a mean of sRGB bytes is not a brightness, which is the whole reason
// this decodes first. #757575 sits at the sRGB midpoint and well below half the light.
func TestRelLuminance(t *testing.T) {
	if got := relLuminance([4]float32{0, 0, 0, 1}); got != 0 {
		t.Errorf("black has luminance %v, want 0", got)
	}
	if got := relLuminance([4]float32{1, 1, 1, 1}); math.Abs(got-1) > 1e-9 {
		t.Errorf("white has luminance %v, want 1", got)
	}
	mid := relLuminance([4]float32{0.4588, 0.4588, 0.4588, 1}) // #757575
	if mid >= 0.25 {
		t.Errorf("mid grey has luminance %v, want well under a quarter of white", mid)
	}
	// Green carries most of the light, blue almost none.
	green, blue := relLuminance([4]float32{0, 1, 0, 1}), relLuminance([4]float32{0, 0, 1, 1})
	if green <= blue {
		t.Errorf("green %v, blue %v: the channel weights are wrong", green, blue)
	}
}
