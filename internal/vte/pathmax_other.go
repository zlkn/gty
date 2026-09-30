//go:build !darwin

package vte

// pathMax is PATH_MAX: no directory a shell can be in is longer. 4096 is Linux's; macOS is
// the exception, see pathmax_darwin.go.
const pathMax = 4096
