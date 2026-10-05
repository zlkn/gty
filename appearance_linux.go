package main

import (
	"context"
	"time"

	"github.com/godbus/dbus/v5"
)

const (
	portalDest     = "org.freedesktop.portal.Desktop"
	portalPath     = "/org/freedesktop/portal/desktop"
	portalSettings = "org.freedesktop.portal.Settings"
	appearanceNS   = "org.freedesktop.appearance"
	colorSchemeKey = "color-scheme"
)

func watchColorScheme(onChange func(dark bool)) (dark, ok bool) {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return false, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	var v dbus.Variant
	err = conn.Object(portalDest, portalPath).
		CallWithContext(ctx, portalSettings+".Read", 0, appearanceNS, colorSchemeKey).Store(&v)
	if err != nil {
		conn.Close()
		return false, false
	}
	if dark, ok = schemeDark(v); !ok {
		conn.Close()
		return false, false
	}

	if err := conn.AddMatchSignal(
		dbus.WithMatchObjectPath(portalPath),
		dbus.WithMatchInterface(portalSettings),
		dbus.WithMatchMember("SettingChanged"),
		dbus.WithMatchArg(0, appearanceNS),
		dbus.WithMatchArg(1, colorSchemeKey),
	); err != nil {
		return dark, true
	}
	signals := make(chan *dbus.Signal, 4)
	conn.Signal(signals)
	go func() {
		for s := range signals {
			if s.Name != portalSettings+".SettingChanged" || len(s.Body) != 3 {
				continue
			}
			if v, ok := s.Body[2].(dbus.Variant); ok {
				if d, ok := schemeDark(v); ok {
					onChange(d)
				}
			}
		}
	}()
	return dark, true
}

func schemeDark(v dbus.Variant) (dark, ok bool) {
	for {
		inner, nested := v.Value().(dbus.Variant)
		if !nested {
			break
		}
		v = inner
	}
	n, ok := v.Value().(uint32)
	return n == 1, ok
}
