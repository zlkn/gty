package main

import (
	"math"
	"testing"

	"github.com/oliverbestmann/webgpu/wgpu"
)

// The only cover the transfer function has without a GPU: render_test.go skips there.

func TestSrgbToLinear(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   float64
		want float64
	}{
		// Fixed points: #000000 and #ffffff must survive the round trip exactly.
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

// TestSrgbToLinearIsMonotonic: an ordering the curve does not preserve reorders the palette.
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

// TestPickFormat: an sRGB variant wins wherever the driver put it, and its order decides
// between two. Deterministic, because the decode is keyed off the result.
func TestPickFormat(t *testing.T) {
	for _, tc := range []struct {
		name string
		want blendSpace
		in   []wgpu.TextureFormat
		out  wgpu.TextureFormat
	}{
		{
			"gamma takes the UNORM past the sRGB Mesa leads with",
			blendGamma,
			[]wgpu.TextureFormat{wgpu.TextureFormatBGRA8UnormSrgb, wgpu.TextureFormatBGRA8Unorm},
			wgpu.TextureFormatBGRA8Unorm,
		},
		{
			"linear takes the sRGB further down the list",
			blendLinear,
			[]wgpu.TextureFormat{wgpu.TextureFormatBGRA8Unorm, wgpu.TextureFormatBGRA8UnormSrgb},
			wgpu.TextureFormatBGRA8UnormSrgb,
		},
		{
			"two that both fit: the driver's order decides",
			blendLinear,
			[]wgpu.TextureFormat{wgpu.TextureFormatRGBA8UnormSrgb, wgpu.TextureFormatBGRA8UnormSrgb},
			wgpu.TextureFormatRGBA8UnormSrgb,
		},
		{
			"gamma with no plain UNORM on offer falls back to the sRGB",
			blendGamma,
			[]wgpu.TextureFormat{wgpu.TextureFormatBGRA8UnormSrgb},
			wgpu.TextureFormatBGRA8UnormSrgb,
		},
		{
			"linear with no sRGB on offer falls back to the UNORM",
			blendLinear,
			[]wgpu.TextureFormat{wgpu.TextureFormatBGRA8Unorm, wgpu.TextureFormatRGBA8Unorm},
			wgpu.TextureFormatBGRA8Unorm,
		},
		{"nothing on offer", blendGamma, nil, wgpu.TextureFormatUndefined},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := pickFormat(tc.in, tc.want); got != tc.out {
				t.Errorf("pickFormat(%v, %v) = %v, want %v", tc.in, tc.want, got, tc.out)
			}
		})
	}
}

// TestPickFormatSkipsFormatsTheDeviceCannotUse: a surface offers more than the two kinds
// this renderer draws into, and the extras need device features that are not enabled —
// choosing one fails pipeline creation with a validation error rather than looking wrong.
func TestPickFormatSkipsFormatsTheDeviceCannotUse(t *testing.T) {
	exotic := []wgpu.TextureFormat{
		wgpu.TextureFormatRGBA16Unorm, // wants TEXTURE_FORMAT_16BIT_NORM
		wgpu.TextureFormatRGBA16Float,
		wgpu.TextureFormatRGB10A2Unorm,
	}
	for _, want := range []blendSpace{blendGamma, blendLinear} {
		t.Run(want.String(), func(t *testing.T) {
			// Exotic first, so taking the driver's lead would pick one.
			offered := append(append([]wgpu.TextureFormat{}, exotic...),
				wgpu.TextureFormatBGRA8Unorm, wgpu.TextureFormatBGRA8UnormSrgb)
			got := pickFormat(offered, want)
			if !isGammaFormat(got) && !isSrgbFormat(got) {
				t.Errorf("pickFormat picked %v, which this renderer cannot draw into", got)
			}
			if spaceOf(got) != want {
				t.Errorf("pickFormat picked %v, blending in %v, want %v", got, spaceOf(got), want)
			}
		})
	}
}

// TestClearValueDecodesOnlyForSrgbTargets: no shader touches the clear, so this is all of
// its correctness.
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

// TestCoverageExponent: the direction is a property of the theme, and the wrong one thins
// the text it is meant to thicken.
func TestCoverageExponent(t *testing.T) {
	light := [4]float32{0.949, 0.949, 0.949, 1} // #f2f2f2
	dark := [4]float32{0.259, 0.259, 0.259, 1}  // #424242

	if got, want := coverageExponent(dark, light, 0, blendLinear), float32(1/darkOnLightGamma); got != want {
		t.Errorf("dark on light, blended linearly: exponent %v, want %v — the edges need thickening", got, want)
	}
	if got := coverageExponent(light, dark, 0, blendLinear); got != 1 {
		t.Errorf("light ink on dark paper: exponent %v, want 1 — linear blending already reads heavy", got)
	}
	// The bend exists to undo what linear blending does; gamma-space needs none of it.
	if got := coverageExponent(dark, light, 0, blendGamma); got != 1 {
		t.Errorf("dark on light, blended in gamma space: exponent %v, want 1", got)
	}

	// The knob wins over the derivation, in either space.
	if got, want := coverageExponent(dark, light, 2, blendLinear), float32(0.5); got != want {
		t.Errorf("gamma 2 gave %v, want %v", got, want)
	}
	if got, want := coverageExponent(dark, light, 2, blendGamma), float32(0.5); got != want {
		t.Errorf("gamma 2 in gamma space gave %v, want %v", got, want)
	}
	if got, want := coverageExponent(light, dark, 1.25, blendLinear), float32(1/1.25); got != want {
		t.Errorf("gamma 1.25 gave %v, want %v", got, want)
	}

	// Clamped, never zero or negative: pow with either would be a screen of NaN.
	for _, gamma := range []float64{-3, 0.01, 1e9} {
		if got := coverageExponent(dark, light, gamma, blendLinear); got <= 0 || got > 4 {
			t.Errorf("gamma %v gave exponent %v, want something a pow can use", gamma, got)
		}
	}
}

// TestRelLuminance: a mean of sRGB bytes is not a brightness — #757575 is the sRGB midpoint
// and well below half the light.
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
