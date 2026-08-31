package main

import (
	"fmt"
	"image"
	"strings"
	"testing"

	"gty/internal/font"
)

// tabOf1 is a tab holding one pane.
func tabOf1(id int) *tab {
	p := newPane(id)
	return &tab{root: &node{pane: p}, focused: p}
}

// TestSplitBar: bar and content tile the surface, a single tab included.
func TestSplitBar(t *testing.T) {
	surface := image.Rect(0, 0, 900, 600)

	for _, scale := range []float64{1, 2} {
		t.Run(fmt.Sprintf("%gx", scale), func(t *testing.T) {
			withScale(t, scale)

			// One tab gets a bar too: the window has to open as it will go on looking.
			if bar, _ := splitBar(surface, 1, testCellH); bar.Dy() != barHeight(testCellH) {
				t.Errorf("one tab took a %v bar, want one %d px tall", bar, barHeight(testCellH))
			}
			if bar, content := splitBar(surface, 0, testCellH); !bar.Empty() || content != surface {
				t.Errorf("no tabs took a %v bar, want none", bar)
			}

			bar, content := splitBar(surface, 3, testCellH)
			if got, want := bar.Dy(), barHeight(testCellH); got != want {
				t.Errorf("the bar is %d px tall, want %d", got, want)
			}
			if bar.Max.Y != content.Min.Y {
				t.Errorf("the bar ends at %d and the content starts at %d; they do not meet",
					bar.Max.Y, content.Min.Y)
			}
			if bar.Dy()+content.Dy() != surface.Dy() {
				t.Errorf("%d + %d px do not tile the %d px surface", bar.Dy(), content.Dy(), surface.Dy())
			}
			if bar.Min.X != surface.Min.X || bar.Max.X != surface.Max.X {
				t.Errorf("the bar spans x %d..%d, want the surface's %d..%d",
					bar.Min.X, bar.Max.X, surface.Min.X, surface.Max.X)
			}
		})
	}
}

// TestSplitBarTinyWindow: no room under a bar means no bar.
func TestSplitBarTinyWindow(t *testing.T) {
	surface := image.Rect(0, 0, 900, barHeight(testCellH)-1)
	if bar, content := splitBar(surface, 3, testCellH); !bar.Empty() || content != surface {
		t.Errorf("a %d px window took a %v bar, want none", surface.Dy(), bar)
	}
}

// TestLayoutBarPacksLeft: the tabs run left to right, each as wide as its own label,
// touching but never overlapping.
func TestLayoutBarPacksLeft(t *testing.T) {
	tabs := []*tab{tabOf1(1), tabOf1(2), tabOf1(3)}
	for i, tb := range tabs {
		tb.focused.title = []string{"fish", "vim", "htop"}[i]
	}
	bar, _ := splitBar(image.Rect(0, 0, 901, 600), len(tabs), testCellH)

	_, labels := layoutBar(tabs, 1, bar, testCellW, testCellH)
	if len(labels) != len(tabs) {
		t.Fatalf("%d tabs produced %d labels", len(tabs), len(labels))
	}

	if want := bar.Min.X + barInset*testCellW; labels[0].rect.Min.X != want {
		t.Errorf("the first tab starts at x %d, want it inset to %d", labels[0].rect.Min.X, want)
	}
	for i := 1; i < len(labels); i++ {
		if labels[i-1].rect.Max.X != labels[i].rect.Min.X {
			t.Errorf("tab %d ends at x %d and tab %d starts at %d",
				i-1, labels[i-1].rect.Max.X, i, labels[i].rect.Min.X)
		}
	}
	if last := labels[len(labels)-1].rect.Max.X; last >= bar.Max.X {
		t.Errorf("three short labels reached x %d of a %d px bar; they were not packed", last, bar.Max.X)
	}
	for i, l := range labels {
		if want := (len(l.cells) + 2*tabPadding) * testCellW; l.rect.Dx() != want {
			t.Errorf("tab %d is %d px for %d cells, want %d", i, l.rect.Dx(), len(l.cells), want)
		}
	}
}

// TestLayoutBarMarksTheActive: bold label, full-strength ink, and an underline painted
// after the divider so that it lands on top of it.
func TestLayoutBarMarksTheActive(t *testing.T) {
	const active = 1
	tabs := []*tab{tabOf1(1), tabOf1(2), tabOf1(3)}
	bar, _ := splitBar(image.Rect(0, 0, 901, 600), len(tabs), testCellH)

	fills, labels := layoutBar(tabs, active, bar, testCellW, testCellH)
	if len(fills) != 3 {
		t.Fatalf("want a divider, its shadow and one underline, got %d fills", len(fills))
	}

	divider, fade, mark := fills[0], fills[1], fills[2]
	if fade.rect.Min.Y != divider.rect.Max.Y || fade.rect.Dx() != divider.rect.Dx() {
		t.Errorf("the second row %v does not sit under the whole divider %v", fade.rect, divider.rect)
	}
	if want := mix(selectionColor, backgroundRGBA, dividerFade); fade.color != want {
		t.Errorf("the second row is %v, want %v — lighter than the line above it", fade.color, want)
	}
	if relLuminance(fade.color) <= relLuminance(divider.color) {
		t.Errorf("the divider is %v over %v; it has to be darker on top", divider.color, fade.color)
	}
	if want := bar.Max.Y - px(dividerPad); divider.rect.Max.Y != want {
		t.Errorf("the divider ends at y %d, want %d — dividerPad clear of the bar's bottom",
			divider.rect.Max.Y, want)
	}
	if fade.rect.Max.Y >= bar.Max.Y {
		t.Errorf("the second row reaches y %d, want air left before the terminal at %d",
			fade.rect.Max.Y, bar.Max.Y)
	}
	if want := bar.Dx() - 2*px(dividerInset); divider.rect.Dx() != want {
		t.Errorf("the divider is %d px wide, want %d — inset at both ends", divider.rect.Dx(), want)
	}
	if divider.rect.Min.X <= bar.Min.X || divider.rect.Max.X >= bar.Max.X {
		t.Errorf("the divider %v touches an edge of the %v bar", divider.rect, bar)
	}

	if mark.rect.Max.Y != divider.rect.Max.Y {
		t.Errorf("the underline %v does not share the divider's baseline %v", mark.rect, divider.rect)
	}
	if mark.rect.Dy() <= divider.rect.Dy() {
		t.Errorf("the underline is %d px and the divider %d; it has to be the thicker",
			mark.rect.Dy(), divider.rect.Dy())
	}
	if mark.rect.Min.X != labels[active].rect.Min.X || mark.rect.Max.X != labels[active].rect.Max.X {
		t.Errorf("the underline spans x %d..%d, the active tab %d..%d",
			mark.rect.Min.X, mark.rect.Max.X, labels[active].rect.Min.X, labels[active].rect.Max.X)
	}
	if mark.color != palette[tabAccent] {
		t.Errorf("the underline is %v, want the accent %v", mark.color, palette[tabAccent])
	}

	for _, c := range labels[active].cells {
		if c.Style != font.Bold {
			t.Fatalf("the active label is drawn in %v, want bold", c.Style)
		}
	}
	for _, c := range labels[0].cells {
		if c.Style != font.Regular {
			t.Fatalf("an inactive label is drawn in %v, want regular", c.Style)
		}
	}
	// Both at full strength: bold and the underline are the whole cue.
	for i, l := range labels {
		if l.fg != foreground {
			t.Errorf("label %d is %v, want the plain foreground %v", i, l.fg, foreground)
		}
	}
}

// TestLayoutBarSharesACrowdedBar: too many tabs to pack fall back to an equal share of
// the bar, still inside it.
func TestLayoutBarSharesACrowdedBar(t *testing.T) {
	tabs := make([]*tab, 9)
	for i := range tabs {
		tabs[i] = tabOf1(i + 1)
		tabs[i].focused.title = "a rather long window title"
	}
	bar, _ := splitBar(image.Rect(0, 0, 600, 600), len(tabs), testCellH)

	_, labels := layoutBar(tabs, 0, bar, testCellW, testCellH)
	// Every one of them: a tab the bar has no room for is a tab you cannot see.
	if len(labels) != len(tabs) {
		t.Fatalf("%d tabs produced %d labels", len(tabs), len(labels))
	}
	for i, l := range labels {
		if !l.rect.In(bar) {
			t.Errorf("tab %d at %v is outside the %v bar", i, l.rect, bar)
		}
	}
	if n, full := len(labels[0].cells), len([]rune(tabs[0].label(0))); n == 0 || n >= full {
		t.Errorf("a crowded tab kept %d of %d cells; it was not clipped", n, full)
	}
}

// TestLayoutBarTooNarrow: no room for a glyph produces no negative geometry.
func TestLayoutBarTooNarrow(t *testing.T) {
	tabs := []*tab{tabOf1(1), tabOf1(2), tabOf1(3)}
	bar := image.Rect(0, 0, 12, barHeight(testCellH))

	fills, labels := layoutBar(tabs, 0, bar, testCellW, testCellH)
	for i, l := range labels {
		if l.rect.Dx() < 0 || l.rect.Dy() < 0 {
			t.Errorf("label %d has the negative rect %v", i, l.rect)
		}
	}
	for _, f := range fills {
		if f.rect.Dx() < 0 || f.rect.Dy() < 0 {
			t.Errorf("fill %v has a negative side", f.rect)
		}
	}
}

// TestLabelCells: too long ends in an ellipsis, and never exceeds the columns given.
func TestLabelCells(t *testing.T) {
	for _, tc := range []struct {
		in   string
		cols int
		want string
	}{
		{"fish", 10, "fish"},
		{"fish", 4, "fish"},
		{"~/Personal/gty", 6, "~/Per…"},
		{"fish", 1, "…"},
		{"fish", 0, ""},
	} {
		cells := labelCells(tc.in, tc.cols)
		var b strings.Builder
		for _, c := range cells {
			b.WriteRune(c.Rune)
		}
		if got := b.String(); got != tc.want {
			t.Errorf("labelCells(%q, %d) is %q, want %q", tc.in, tc.cols, got, tc.want)
		}
		if len(cells) > tc.cols {
			t.Errorf("labelCells(%q, %d) is %d cells wide", tc.in, tc.cols, len(cells))
		}
	}
}

// TestActiveAfterClose: the display follows the shift, and never lands off the end.
func TestActiveAfterClose(t *testing.T) {
	for _, tc := range []struct{ closed, active, left, want int }{
		{closed: 0, active: 2, left: 3, want: 1}, // ahead of it: everything shifted down
		{closed: 2, active: 0, left: 3, want: 0}, // behind it: nothing moved
		{closed: 1, active: 1, left: 3, want: 1}, // itself: the tab that took its index
		{closed: 3, active: 3, left: 3, want: 2}, // itself, and it was the last one
	} {
		if got := activeAfterClose(tc.closed, tc.active, tc.left); got != tc.want {
			t.Errorf("closing %d with %d active and %d left lands on %d, want %d",
				tc.closed, tc.active, tc.left, got, tc.want)
		}
	}
}

// TestBackgroundTabKeepsItsGrid: the invariant that keeps a switch from being a
// SIGWINCH to every shell in the tab.
func TestBackgroundTabKeepsItsGrid(t *testing.T) {
	surface := image.Rect(0, 0, 900, 600)
	tabs := []*tab{tabOf1(1), tabOf1(2), tabOf1(3)}
	_, content := splitBar(surface, len(tabs), testCellH)

	for _, tb := range tabs {
		tb.panes, tb.dividers = layoutTree(tb.root, content, testCellW, testCellH)
	}

	want := [2]int{tabs[0].panes[0].cols, tabs[0].panes[0].rows}
	if want[0] == 0 || want[1] == 0 {
		t.Fatalf("the tabs came out %v cells, so this test proves nothing", want)
	}
	for i, tb := range tabs {
		if got := [2]int{tb.panes[0].cols, tb.panes[0].rows}; got != want {
			t.Errorf("tab %d is %v cells, tab 0 is %v", i, got, want)
		}
	}
}

// TestOSCTitle: OSC 0 or 2 names the tab, terminated either way.
func TestOSCTitle(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"osc 2 ends with ST", "\x1b]2;editing layout.go\x1b\\", "editing layout.go"},
		{"osc 0 ends with BEL", "\x1b]0;~/Personal/gty\a", "~/Personal/gty"},
		{"a DEL is not part of a title", "\x1b]2;one\x7ftwo\x1b\\", "onetwo"},
		{"a colour query is not a title", "\x1b]11;?\x1b\\", ""},
		{"an empty title clears it", "\x1b]2;\x1b\\", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if p := vtPane(40, 4, tc.in); p.title != tc.want {
				t.Errorf("%q left the title %q, want %q", tc.in, p.title, tc.want)
			}
		})
	}
}

// TestCleanTitleCaps: a shell cannot hang an unbounded string on a tab.
func TestCleanTitleCaps(t *testing.T) {
	if got := len([]rune(cleanTitle(strings.Repeat("x", maxTitle+50)))); got != maxTitle {
		t.Errorf("a title of %d runes came back as %d, want it capped at %d", maxTitle+50, got, maxTitle)
	}
}

// TestTabLabelFallback: the number stands in until the shell sets a title.
func TestTabLabelFallback(t *testing.T) {
	tb := tabOf1(1)
	if got, want := tb.label(2), "3: shell"; got != want {
		t.Errorf("an unnamed tab is called %q, want %q", got, want)
	}
	tb.focused.title = "htop"
	if got := tb.label(2); got != "htop" {
		t.Errorf("a named tab is called %q, want %q", got, "htop")
	}
}
