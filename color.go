package main

import "gty/internal/vte"

// resolveColor turns a packed terminal colour into what the renderer wants. dflt is the
// theme's own foreground or background, which is what a default cell is asking for.
func resolveColor(c vte.Color, dflt [4]float32) [4]float32 {
	if i, ok := c.Palette(); ok {
		return palette[i]
	}
	if r, g, b, ok := c.RGB(); ok {
		return [4]float32{float32(r) / 255, float32(g) / 255, float32(b) / 255, 1}
	}
	return dflt
}

// base16 is the named end of the palette, copied out of the active theme by applyTheme.
var base16 [16]uint32

// palette is the 256-colour table: the sixteen named colours, then the 6x6x6 cube and
// the 24-step grey ramp exactly as xterm defines them, so an index means the same here
// as everywhere else. refreshTheme fills it, because the config file may replace the
// named end of it.
var palette [256][4]float32

func buildPalette() [256][4]float32 {
	var p [256][4]float32
	rgb := func(v uint32) [4]float32 {
		return [4]float32{
			float32(v>>16&0xFF) / 255,
			float32(v>>8&0xFF) / 255,
			float32(v&0xFF) / 255,
			1,
		}
	}
	for i, v := range base16 {
		p[i] = rgb(v)
	}

	levels := [6]uint32{0, 95, 135, 175, 215, 255}
	i := 16
	for _, r := range levels {
		for _, g := range levels {
			for _, b := range levels {
				p[i] = rgb(r<<16 | g<<8 | b)
				i++
			}
		}
	}
	for g := range 24 {
		v := uint32(8 + 10*g)
		p[i] = rgb(v<<16 | v<<8 | v)
		i++
	}
	return p
}
