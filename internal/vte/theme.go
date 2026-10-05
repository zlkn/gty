package vte

import "fmt"

func (t *Terminal) ThemeReport(dark bool) []byte {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if !t.themeReports {
		return nil
	}
	return []byte(themeReport(dark))
}

func themeReport(dark bool) string {
	if dark {
		return "\x1b[?997;1n"
	}
	return "\x1b[?997;2n"
}

func (t *Terminal) reportMode(mode int) {
	if mode != 2031 {
		return
	}
	state := 2
	if t.themeReports {
		state = 1
	}
	t.reply(fmt.Sprintf("\x1b[?%d;%d$y", mode, state))
}
