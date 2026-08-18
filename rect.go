package main

import (
	_ "embed"
	"fmt"
	"image"
	"os"
	"unsafe"

	"github.com/oliverbestmann/webgpu/wgpu"
)

//go:embed rect.wgsl
var rectShader string

const initialRects = 64

// rectInstance is one solid quad. Layout must match the @location bindings in
// rect.wgsl.
type rectInstance struct {
	rect  [4]float32 // x, y, w, h in px
	color [4]float32
}

// rects draws flat colour quads: the pane dividers today, the cursor and the
// selection later.
type rects struct {
	device *wgpu.Device
	queue  *wgpu.Queue

	pipeline  *wgpu.RenderPipeline
	bindGroup *wgpu.BindGroup
	uniform   *wgpu.Buffer
	vertexBuf *wgpu.Buffer

	instances []rectInstance
	bufCap    int // vertexBuf capacity, in instances
}

func newRects(device *wgpu.Device, queue *wgpu.Queue, format wgpu.TextureFormat) (r *rects, err error) {
	defer func() {
		if err != nil {
			r.release()
			r = nil
		}
	}()
	r = &rects{device: device, queue: queue}

	// Its own viewport uniform rather than the text renderer's: 16 bytes buys two
	// pipelines that do not care which of them draws first.
	if r.uniform, err = device.TryCreateBuffer(&wgpu.BufferDescriptor{
		Label: "Rect Viewport Uniform",
		Size:  4 * 4,
		Usage: wgpu.BufferUsageUniform | wgpu.BufferUsageCopyDst,
	}); err != nil {
		return r, err
	}
	if r.vertexBuf, err = device.TryCreateBuffer(&wgpu.BufferDescriptor{
		Label: "Rect Instances",
		Size:  uint64(initialRects * unsafe.Sizeof(rectInstance{})),
		Usage: wgpu.BufferUsageVertex | wgpu.BufferUsageCopyDst,
	}); err != nil {
		return r, err
	}
	r.bufCap = initialRects

	shader, err := device.TryCreateShaderModule(&wgpu.ShaderModuleDescriptor{
		Label:      "rect.wgsl",
		WGSLSource: &wgpu.ShaderSourceWGSL{Code: rectShader},
	})
	if err != nil {
		return r, err
	}
	defer shader.Release()

	r.pipeline, err = device.TryCreateRenderPipeline(&wgpu.RenderPipelineDescriptor{
		Label: "Rect Pipeline",
		Vertex: wgpu.VertexState{
			Module:     shader,
			EntryPoint: "vs_main",
			Buffers: []wgpu.VertexBufferLayout{{
				ArrayStride: uint64(unsafe.Sizeof(rectInstance{})),
				StepMode:    wgpu.VertexStepModeInstance,
				Attributes: []wgpu.VertexAttribute{
					{Format: wgpu.VertexFormatFloat32x4, Offset: 0, ShaderLocation: 0},
					{Format: wgpu.VertexFormatFloat32x4, Offset: 16, ShaderLocation: 1},
				},
			}},
		},
		Fragment: &wgpu.FragmentState{
			Module:     shader,
			EntryPoint: "fs_main",
			Targets: []wgpu.ColorTargetState{{
				Format: format,
				// Blending although dividers are opaque, so a translucent selection
				// can reuse this pipeline unchanged.
				Blend: &wgpu.BlendState{
					Color: wgpu.BlendComponent{
						SrcFactor: wgpu.BlendFactorSrcAlpha,
						DstFactor: wgpu.BlendFactorOneMinusSrcAlpha,
						Operation: wgpu.BlendOperationAdd,
					},
					Alpha: wgpu.BlendComponent{
						SrcFactor: wgpu.BlendFactorOne,
						DstFactor: wgpu.BlendFactorOneMinusSrcAlpha,
						Operation: wgpu.BlendOperationAdd,
					},
				},
				WriteMask: wgpu.ColorWriteMaskAll,
			}},
		},
		Primitive: wgpu.PrimitiveState{
			Topology: wgpu.PrimitiveTopologyTriangleStrip,
			CullMode: wgpu.CullModeNone,
		},
		Multisample: wgpu.MultisampleState{Count: 1, Mask: 0xFFFFFFFF},
	})
	if err != nil {
		return r, err
	}

	layout := r.pipeline.GetBindGroupLayout(0)
	defer layout.Release()
	r.bindGroup, err = device.TryCreateBindGroup(&wgpu.BindGroupDescriptor{
		Label:  "Rect Bind Group",
		Layout: layout,
		Entries: []wgpu.BindGroupEntry{
			{Binding: 0, Buffer: r.uniform, Size: wgpu.WholeSize},
		},
	})
	if err != nil {
		return r, err
	}
	return r, nil
}

// Set replaces the quad list. Empty rects are dropped: a split too narrow to
// divide produces them.
func (r *rects) Set(quads []image.Rectangle, color [4]float32) {
	r.instances = r.instances[:0]
	for _, q := range quads {
		if q.Empty() {
			continue
		}
		r.instances = append(r.instances, rectInstance{
			rect:  [4]float32{float32(q.Min.X), float32(q.Min.Y), float32(q.Dx()), float32(q.Dy())},
			color: color,
		})
	}
	r.upload()
}

func (r *rects) upload() {
	if need := len(r.instances); need > r.bufCap {
		buf, n, err := growBuffer(r.device, "Rect Instances", r.vertexBuf,
			wgpu.BufferUsageVertex|wgpu.BufferUsageCopyDst, r.bufCap, need, unsafe.Sizeof(rectInstance{}))
		if err != nil {
			fmt.Fprintf(os.Stderr, "gty: grow rect buffer to %d: %v; rects clipped\n", need, err)
			r.instances = r.instances[:r.bufCap]
		} else {
			r.vertexBuf, r.bufCap = buf, n
		}
	}
	if len(r.instances) == 0 {
		return
	}
	r.queue.WriteBuffer(r.vertexBuf, 0, wgpu.ToBytes(r.instances))
}

// Draw sets the scissor to the whole surface, so it does not depend on running
// before the text renderer, which leaves it clipped to the last pane it drew.
func (r *rects) Draw(pass *wgpu.RenderPassEncoder, viewportW, viewportH uint32) {
	if len(r.instances) == 0 {
		return
	}
	r.queue.WriteBuffer(r.uniform, 0, wgpu.ToBytes([]float32{
		float32(viewportW), float32(viewportH), 0, 0,
	}))
	pass.SetScissorRect(0, 0, viewportW, viewportH)
	pass.SetPipeline(r.pipeline)
	pass.SetBindGroup(0, r.bindGroup, nil)
	pass.SetVertexBuffer(0, r.vertexBuf, 0, wgpu.WholeSize)
	pass.Draw(4, uint32(len(r.instances)), 0, 0)
}

// release is idempotent, for the same reason text.release is.
func (r *rects) release() {
	if r == nil {
		return
	}
	if r.bindGroup != nil {
		r.bindGroup.Release()
		r.bindGroup = nil
	}
	if r.pipeline != nil {
		r.pipeline.Release()
		r.pipeline = nil
	}
	if r.vertexBuf != nil {
		r.vertexBuf.Release()
		r.vertexBuf, r.bufCap = nil, 0
	}
	if r.uniform != nil {
		r.uniform.Release()
		r.uniform = nil
	}
}
