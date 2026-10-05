package main

import "github.com/go-gl/glfw/v3.4/glfw"

type theme struct {
	background, foreground, selection [4]float32
	cursor                            *[4]float32
	base16                            [16]uint32
}

// lightTheme's palette is for a light background — which means the two
// ends swap roles: ANSI black is a light shade, and ANSI white is the ink a program gets
// when it asks for the brightest thing it knows of.
//
// The six chromatic colours are dark enough to read on the paper, 5:1 and better. The
// palette this replaces was picked for a #1a1b20 background and left behind when the
// theme went light: on #f2f2f2 its green sat at 1.80:1 and its bright white at 1.12:1,
// so a coloured ls listing was barely there and the brightest colour was invisible.
//
// Bright repeats them, black and white apart. A light theme has no headroom to brighten
// into, and lightening a colour here would only take contrast away.
var lightTheme = theme{
	background: [4]float32{0.949, 0.949, 0.949, 1}, // #f2f2f2
	foreground: [4]float32{0.259, 0.259, 0.259, 1}, // #424242
	selection:  [4]float32{0.851, 0.851, 0.851, 1}, // #d9d9d9
	base16: [16]uint32{
		0xd1d1d1, 0xb81a6b, 0x1e763c, 0x8d5b00, 0x015493, 0x75228e, 0x007474, 0x424242,
		0x57606a, 0xb81a6b, 0x1e763c, 0x8d5b00, 0x015493, 0x75228e, 0x007474, 0x085157,
	},
}

var darkTheme = theme{
	background: [4]float32{0.180, 0.180, 0.196, 1}, // #2e2e32
	foreground: [4]float32{0.631, 0.631, 0.631, 1}, // #a1a1a1
	selection:  [4]float32{0.271, 0.271, 0.294, 1}, // #45454b
	base16: [16]uint32{
		0x1a1b20, 0xe06c75, 0x98c379, 0xe5c07b, 0x61afef, 0xc678dd, 0x56b6c2, 0xd9dee7,
		0x7f848e, 0xef8b93, 0xb3d99a, 0xf0d3a0, 0x8cc4f5, 0xd79ae8, 0x7fcdd7, 0xffffff,
	},
}

var (
	darkMode     bool
	followSystem = true
)

func currentTheme() *theme {
	if darkMode {
		return &darkTheme
	}
	return &lightTheme
}

func applyTheme() {
	th := currentTheme()
	backgroundRGBA, foreground, selectionColor = th.background, th.foreground, th.selection
	cursorTint, base16 = th.cursor, th.base16
	refreshTheme()
}

func (a *app) setDark(dark bool) {
	if dark == darkMode {
		return
	}
	darkMode = dark
	applyTheme()
	for _, t := range a.tabs {
		for _, p := range t.panes {
			a.send(p, p.term.ThemeReport(dark))
		}
	}
	a.Damage()
}

func (a *app) syncSystemTheme() {
	if a.systemChanged.Swap(false) && followSystem {
		a.setDark(a.systemDark.Load())
	}
}

func (a *app) onSystemScheme(dark bool) {
	a.systemDark.Store(dark)
	a.systemChanged.Store(true)
	glfw.PostEmptyEvent()
}
