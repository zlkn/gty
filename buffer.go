package main

import "github.com/oliverbestmann/webgpu/wgpu"

// growBuffer doubles buf from have until it holds need elements of elem bytes,
// returning the old pair unchanged on failure.
//
// Releasing the old buffer is safe with a frame in flight: a submitted command
// buffer that still references it holds its own reference.
func growBuffer(device *wgpu.Device, label string, buf *wgpu.Buffer, usage wgpu.BufferUsage,
	have, need int, elem uintptr) (*wgpu.Buffer, int, error) {
	n := max(have, 1)
	for n < need {
		n *= 2
	}
	grown, err := device.TryCreateBuffer(&wgpu.BufferDescriptor{
		Label: label,
		Size:  uint64(n) * uint64(elem),
		Usage: usage,
	})
	if err != nil {
		return buf, have, err
	}
	buf.Release()
	return grown, n, nil
}
