package main

// color is a packed terminal colour: a kind in the top byte and its argument below, so
// the zero value is "whatever the theme calls default" and a cleared cell costs no
// initialising.
type color uint32

const (
	kindDefault = iota
	kindPalette
	kindRGB

	kindShift = 24

	colorDefault color = kindDefault << kindShift
)

func paletteColor(i int) color { return kindPalette<<kindShift | color(i&0xFF) }

func rgbColor(r, g, b int) color {
	return kindRGB<<kindShift | color(uint8(r))<<16 | color(uint8(g))<<8 | color(uint8(b))
}

// resolve turns a packed colour into what the renderer wants. dflt is the theme's own
// foreground or background, which is what a default cell is asking for.
func (c color) resolve(dflt [4]float32) [4]float32 {
	switch c >> kindShift {
	case kindPalette:
		return palette[c&0xFF]
	case kindRGB:
		return [4]float32{
			float32(c>>16&0xFF) / 255,
			float32(c>>8&0xFF) / 255,
			float32(c&0xFF) / 255,
			1,
		}
	}
	return dflt
}

// base16 is the named end of the palette, picked to sit with the theme rather than
// taken from xterm's washed-out defaults.
var base16 = [16]uint32{
	0x1a1b20, 0xe06c75, 0x98c379, 0xe5c07b, 0x61afef, 0xc678dd, 0x56b6c2, 0xd9dee7,
	0x5c6370, 0xef8b93, 0xb3d99a, 0xf0d3a0, 0x8cc4f5, 0xd79ae8, 0x7fcdd7, 0xffffff,
}

// palette is the 256-colour table: the sixteen named colours, then the 6x6x6 cube and
// the 24-step grey ramp exactly as xterm defines them, so an index means the same here
// as everywhere else.
var palette = buildPalette()

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
