package main

import (
	"image"
	"image/png"
	"os"
	"testing"

	"github.com/oliverbestmann/webgpu/wgpu"
)

// TestRenderToPNG draws the same frame main() draws, but into an offscreen
// texture, and reads it back so the result can be looked at.
func TestRenderToPNG(t *testing.T) {
	const (
		width  = 576 // 320*4 = 1280 bytes/row, a multiple of the required 256
		height = 200
		format = wgpu.TextureFormatRGBA8Unorm
	)

	instance := wgpu.CreateInstance(nil)
	defer instance.Release()

	adapter, err := instance.RequestAdapter(nil)
	if err != nil {
		t.Skipf("no adapter: %v", err)
	}
	defer adapter.Release()

	device, err := adapter.RequestDevice(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer device.Release()
	queue := device.GetQueue()

	txt, err := newText(device, queue, format, fontSize)
	if err != nil {
		t.Fatal(err)
	}
	defer txt.release()
	txt.Set(HELLO_WORLD, padding, foreground)
	t.Logf("instances: %d", len(txt.instances))

	extent := wgpu.Extent3D{Width: width, Height: height, DepthOrArrayLayers: 1}
	target, err := device.TryCreateTexture(&wgpu.TextureDescriptor{
		Size:          extent,
		MipLevelCount: 1,
		SampleCount:   1,
		Dimension:     wgpu.TextureDimension2D,
		Format:        format,
		Usage:         wgpu.TextureUsageRenderAttachment | wgpu.TextureUsageCopySrc,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer target.Release()
	view, err := target.TryCreateView(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer view.Release()

	const bytesPerRow = width * 4
	out, err := device.TryCreateBuffer(&wgpu.BufferDescriptor{
		Size:  bytesPerRow * height,
		Usage: wgpu.BufferUsageMapRead | wgpu.BufferUsageCopyDst,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer out.Release()

	encoder, err := device.TryCreateCommandEncoder(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer encoder.Release()

	pass := encoder.BeginRenderPass(&wgpu.RenderPassDescriptor{
		ColorAttachments: []wgpu.RenderPassColorAttachment{{
			View:       view,
			LoadOp:     wgpu.LoadOpClear,
			StoreOp:    wgpu.StoreOpStore,
			ClearValue: background,
		}},
	})
	txt.Draw(pass, width, height)
	pass.End()
	pass.Release()

	encoder.TryCopyTextureToBuffer(
		target.AsImageCopy(),
		&wgpu.TexelCopyBufferInfo{
			Buffer: out,
			Layout: wgpu.TexelCopyBufferLayout{BytesPerRow: bytesPerRow, RowsPerImage: wgpu.CopyStrideUndefined},
		},
		&extent,
	)

	cmd, err := encoder.TryFinish(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cmd.Release()
	queue.Submit(cmd)

	out.TryMapAsync(wgpu.MapModeRead, 0, bytesPerRow*height, func(status wgpu.MapAsyncStatus) {
		if status != wgpu.MapAsyncStatusSuccess {
			t.Errorf("map failed: %v", status)
		}
	})
	defer out.TryUnmap()
	device.Poll(true, nil)

	img := &image.RGBA{
		Pix:    out.GetMappedRange(0, bytesPerRow*height),
		Stride: bytesPerRow,
		Rect:   image.Rect(0, 0, width, height),
	}

	lit := 0
	for y := range height {
		for x := range width {
			if img.RGBAAt(x, y).R > 100 {
				lit++
			}
		}
	}
	t.Logf("pixels brighter than the background: %d", lit)
	if lit == 0 {
		t.Error("nothing was drawn")
	}

	f, _ := os.Create("/tmp/gty-frame.png")
	defer f.Close()
	png.Encode(f, img)
}
