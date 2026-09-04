//go:build !darwin

package main

// baseDPI is what a point is worth before the display's scale is applied. 96 is Windows'
// logical inch, which CSS and X11 inherited; macOS is the exception, see dpi_darwin.go.
const baseDPI = 96.0
