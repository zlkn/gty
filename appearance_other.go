//go:build !linux

package main

func watchColorScheme(func(dark bool)) (dark, ok bool) { return false, false }
