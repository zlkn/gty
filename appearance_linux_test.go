package main

import (
	"testing"

	"github.com/godbus/dbus/v5"
)

func TestSchemeDark(t *testing.T) {
	tests := []struct {
		name     string
		in       dbus.Variant
		dark, ok bool
	}{
		{"no preference", dbus.MakeVariant(uint32(0)), false, true},
		{"dark", dbus.MakeVariant(uint32(1)), true, true},
		{"light", dbus.MakeVariant(uint32(2)), false, true},
		{"nested by Read", dbus.MakeVariant(dbus.MakeVariant(uint32(1))), true, true},
		{"not a number", dbus.MakeVariant("dark"), false, false},
	}
	for _, tt := range tests {
		dark, ok := schemeDark(tt.in)
		if dark != tt.dark || ok != tt.ok {
			t.Errorf("%s: got %v, %v, want %v, %v", tt.name, dark, ok, tt.dark, tt.ok)
		}
	}
}
