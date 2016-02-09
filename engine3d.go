package main

import (
	"fmt"
	"ndsemu/emu"
	"ndsemu/emu/gfx"
	log "ndsemu/emu/logger"
	"os"
	"sync"
)

var mod3d = log.NewModule("e3d")

// Swap buffers (marker of end-of-frame, with double-buffering)
type E3DCmd_SwapBuffers struct {
}

// New viewport, in pixel coordinates (0-255 / 0-191)
type E3DCmd_SetViewport struct {
	vx0, vy0, vx1, vy1 int
}

// New vertex to be pushed in Vertex RAM, with coordinates in
// clip space (after model-view-proj)
type E3DCmd_Vertex struct {
	x, y, z, w emu.Fixed12
}

// New polygon to be pushed in Polygon RAM
type E3DCmd_Polygon struct {
	vtx  [4]int // indices of vertices in Vertex RAM
	attr uint32 // misc flags
}

type RenderVertexFlags uint32

const (
	RVFClipLeft RenderVertexFlags = 1 << iota
	RVFClipRight
	RVFClipTop
	RVFClipBottom
	RVFClipNear
	RVFClipFar
	RVFTransformed // vertex has been already transformed to screen space

	RVFClipAnything = (RVFClipLeft | RVFClipRight | RVFClipTop | RVFClipBottom | RVFClipNear | RVFClipFar)
)

type RenderVertex struct {
	// Coordinates in clip-space
	cx, cy, cz, cw emu.Fixed12

	flags RenderVertexFlags

	// Screen coordinates
	sx, sy, sz int32
}

// NOTE: these flags match the polygon attribute word defined in the
// geometry coprocessor.
type RenderPolygonFlags uint32

const (
	RPFQuad RenderPolygonFlags = 1 << 31
)

type RenderPolygon struct {
	vtx   [4]int
	flags RenderPolygonFlags

	// y coordinate of middle vertex
	hy int32

	// Slopes between vertices (left/right, top-half/bottom-half)
	dl0, dl1 emu.Fixed12
	dr0, dr1 emu.Fixed12

	// Current segment
	cx0, cx1 emu.Fixed12
}

type HwEngine3d struct {
	// Current viewport (last received viewport command)
	viewport E3DCmd_SetViewport
	// plinecnt [192]cnt
	// plines   [1024][192]int

	vertexRams [2][4096]RenderVertex
	polyRams   [2][4096]RenderPolygon

	// Current vram/pram (being drawn)
	curVram []RenderVertex
	curPram []RenderPolygon

	// Next vram/pram (being accumulated for next frame)
	nextVram []RenderVertex
	nextPram []RenderPolygon

	framecnt  int
	frameLock sync.Mutex

	// Channel to receive new commands
	CmdCh chan interface{}
}

func NewHwEngine3d() *HwEngine3d {
	e3d := new(HwEngine3d)
	e3d.nextVram = e3d.vertexRams[0][:0]
	e3d.nextPram = e3d.polyRams[0][:0]
	e3d.CmdCh = make(chan interface{}, 1024)
	go e3d.recvCmd()
	return e3d
}

func (e3d *HwEngine3d) recvCmd() {
	for {
		cmdi := <-e3d.CmdCh
		switch cmd := cmdi.(type) {
		case E3DCmd_SwapBuffers:
			e3d.cmdSwapBuffers()
		case E3DCmd_SetViewport:
			e3d.viewport = cmd
		case E3DCmd_Polygon:
			e3d.cmdPolygon(cmd)
		case E3DCmd_Vertex:
			e3d.cmdVertex(cmd)
		default:
			panic("invalid command received in HwEnginge3D")
		}
	}
}

func (e3d *HwEngine3d) cmdVertex(cmd E3DCmd_Vertex) {
	vtx := RenderVertex{
		cx: cmd.x,
		cy: cmd.y,
		cz: cmd.z,
		cw: cmd.w,
	}

	// Compute clipping flags (once per vertex)
	if vtx.cx.V < -vtx.cw.V {
		vtx.flags |= RVFClipLeft
	}
	if vtx.cx.V > vtx.cw.V {
		vtx.flags |= RVFClipRight
	}
	if vtx.cy.V < -vtx.cw.V {
		vtx.flags |= RVFClipTop
	}
	if vtx.cy.V > vtx.cw.V {
		vtx.flags |= RVFClipBottom
	}
	// if vtx.cz.V < 0 {
	// 	vtx.flags |= RVFClipNear
	// }
	// if vtx.cz.V > vtx.cw.V {
	// 	vtx.flags |= RVFClipFar
	// }

	// If w==0, we just flag the vertex as fully outside of the screen
	// FIXME: properly handle invalid inputs
	if vtx.cw.V == 0 {
		vtx.flags |= RVFClipAnything
	}

	e3d.nextVram = append(e3d.nextVram, vtx)
}

func (e3d *HwEngine3d) cmdPolygon(cmd E3DCmd_Polygon) {
	poly := RenderPolygon{
		vtx:   cmd.vtx,
		flags: RenderPolygonFlags(cmd.attr),
	}

	// FIXME: for now, skip all polygons outside the screen
	count := 3
	if poly.flags&RPFQuad != 0 {
		count = 4
	}
	clipping := false
	for i := 0; i < count; i++ {
		if poly.vtx[i] >= len(e3d.nextVram) || poly.vtx[i] < 0 {
			mod3d.Fatalf("wrong polygon index: %d (num vtx: %d)", poly.vtx[i], len(e3d.nextVram))
		}
		vtx := e3d.nextVram[poly.vtx[i]]
		if vtx.flags&RVFClipAnything != 0 {
			clipping = true
			break
		}
	}

	if clipping {
		// FIXME: implement clipping
		return
	}

	// Transform all vertices (that weren't transformed already)
	for i := 0; i < count; i++ {
		e3d.vtxTransform(&e3d.nextVram[poly.vtx[i]])
	}

	if count == 4 {
		// Since we're done with clipping, split quad in two
		// triangles, to make the renderer only care about
		// triangles.
		p1, p2 := poly, poly

		p1.flags &^= RPFQuad
		p2.flags &^= RPFQuad
		p2.vtx[1] = p2.vtx[3]

		e3d.nextPram = append(e3d.nextPram, p1, p2)
	} else {
		e3d.nextPram = append(e3d.nextPram, poly)
	}
}

func (e3d *HwEngine3d) vtxTransform(vtx *RenderVertex) {
	if vtx.flags&RVFTransformed != 0 {
		return
	}

	viewwidth := emu.NewFixed12(int32(e3d.viewport.vx1 - e3d.viewport.vx0 + 1))
	viewheight := emu.NewFixed12(int32(e3d.viewport.vy1 - e3d.viewport.vy0 + 1))
	// Compute viewsize / (2*v.w) in two steps, to avoid overflows
	// (viewwidth could be 256<<12, which would overflow when further
	// shifted in preparation for division)
	dx := viewwidth.Div(2).DivFixed(vtx.cw)
	dy := viewheight.Div(2).DivFixed(vtx.cw)

	// sx = (v.x + v.w) * viewwidth / (2*v.w) + viewx0
	// sy = (v.y + v.w) * viewheight / (2*v.w) + viewy0
	vtx.sx = vtx.cx.AddFixed(vtx.cw).MulFixed(dx).Add(int32(e3d.viewport.vx0)).ToInt32()
	vtx.sy = vtx.cy.AddFixed(vtx.cw).MulFixed(dy).Add(int32(e3d.viewport.vy0)).ToInt32()
	vtx.sz = vtx.cz.AddFixed(vtx.cw).Div(2).DivFixed(vtx.cw).ToInt32()

	vtx.flags |= RVFTransformed
}

func (e3d *HwEngine3d) preparePolys() {

	for idx := range e3d.nextPram {
		poly := &e3d.nextPram[idx]
		v0, v1, v2 := &e3d.nextVram[poly.vtx[0]], &e3d.nextVram[poly.vtx[1]], &e3d.nextVram[poly.vtx[2]]

		// Sort the three vertices by the Y coordinate (v0=top, v1=middle, 2=bottom)
		if v0.sy > v1.sy {
			v0, v1 = v1, v0
			poly.vtx[0], poly.vtx[1] = poly.vtx[1], poly.vtx[0]
		}
		if v0.sy > v2.sy {
			v0, v2 = v2, v0
			poly.vtx[0], poly.vtx[2] = poly.vtx[2], poly.vtx[0]
		}
		if v1.sy > v2.sy {
			v1, v2 = v2, v1
			poly.vtx[1], poly.vtx[2] = poly.vtx[2], poly.vtx[1]
		}

		hy1 := v1.sy - v0.sy
		hy2 := v2.sy - v1.sy
		if hy1 < 0 || hy2 < 0 {
			panic("invalid y order")
		}

		poly.cx0, poly.cx1 = emu.NewFixed12(v0.sx), emu.NewFixed12(v0.sx)

		// Calculate the four slopes (two of which are identical, but we don't care)
		// Assume middle vertex is on the left, then swap if that's not the case
		if hy1 > 0 {
			poly.dl0 = emu.NewFixed12(v1.sx - v0.sx).Div(hy1)
		} else {
			poly.dl0 = emu.NewFixed12(v1.sx - v0.sx)
		}
		if hy2 > 0 {
			poly.dl1 = emu.NewFixed12(v2.sx - v1.sx).Div(hy2)
		} else {
			poly.dl1 = emu.NewFixed12(v2.sx - v1.sx)
		}
		if hy1+hy2 > 0 {
			poly.dr0 = emu.NewFixed12(v2.sx - v0.sx).Div(hy1 + hy2)
			poly.dr1 = poly.dr0
		}
		if poly.dl0.V > poly.dr0.V {
			poly.dl0, poly.dr0 = poly.dr0, poly.dl0
			poly.dl1, poly.dr1 = poly.dr1, poly.dl1
		}
		if hy1 == 0 {
			poly.cx0 = poly.cx0.AddFixed(poly.dl0)
			poly.cx1 = poly.cx1.AddFixed(poly.dr0)
		}

		poly.hy = v1.sy
	}
}

func (e3d *HwEngine3d) dumpNextScene() {
	if e3d.framecnt == 0 {
		os.Remove("dump3d.txt")
	}
	f, err := os.OpenFile("dump3d.txt", os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0666)
	if err != nil {
		panic(err)
	}
	defer f.Close()

	fmt.Fprintf(f, "begin scene\n")
	for idx, poly := range e3d.nextPram {
		v0 := &e3d.nextVram[poly.vtx[0]]
		v1 := &e3d.nextVram[poly.vtx[1]]
		v2 := &e3d.nextVram[poly.vtx[2]]
		fmt.Fprintf(f, "tri %d:\n", idx)
		fmt.Fprintf(f, "    ccoord: (%v,%v,%v,%v)-(%v,%v,%v,%v)-(%v,%v,%v,%v)\n",
			v0.cx, v0.cy, v0.cz, v0.cw,
			v1.cx, v1.cy, v1.cz, v1.cw,
			v2.cx, v2.cy, v2.cz, v2.cw)
		fmt.Fprintf(f, "    scoord: (%v,%v)-(%v,%v)-(%v,%v)\n",
			v0.sx, v0.sy,
			v1.sx, v1.sy,
			v2.sx, v2.sy)
	}
	mod3d.Infof("end scene")
}

func (e3d *HwEngine3d) cmdSwapBuffers() {
	// The next frame primitives are complete; we can now do full-frame processing
	// in preparation for drawing next frame
	e3d.preparePolys()
	e3d.dumpNextScene()

	// Now wait for the current frame to be fully drawn,
	// because we don't want to mess with buffers being drawn
	e3d.frameLock.Lock()
	e3d.framecnt++
	e3d.curVram = e3d.nextVram
	e3d.curPram = e3d.nextPram
	e3d.nextVram = e3d.vertexRams[e3d.framecnt&1][:0]
	e3d.nextPram = e3d.polyRams[e3d.framecnt&1][:0]
	e3d.frameLock.Unlock()
}

func (e3d *HwEngine3d) Draw3D(ctx *gfx.LayerCtx, lidx int, y int) {

	// Compute which polygon is visible on each screen line; this will be used
	// as a fast lookup table when later we iterate on each line
	var polyPerLine [192][]uint16
	for idx := range e3d.curPram {
		poly := &e3d.curPram[idx]
		v0, _, v2 := &e3d.curVram[poly.vtx[0]], &e3d.curVram[poly.vtx[1]], &e3d.curVram[poly.vtx[2]]
		for y := v0.sy; y <= v2.sy; y++ {
			polyPerLine[y] = append(polyPerLine[y], uint16(idx))
		}
	}

	for {
		line := ctx.NextLine()
		if line.IsNil() {
			return
		}

		for _, idx := range polyPerLine[y] {
			poly := &e3d.curPram[idx]

			for x := poly.cx0.ToInt32(); x <= poly.cx1.ToInt32(); x++ {
				if x < 0 || x >= 256 {
					continue //panic("out of bounds")
				}
				line.Set16(int(x), 0xFFFF)
			}

			if int32(y) < poly.hy {
				poly.cx0 = poly.cx0.AddFixed(poly.dl0)
				poly.cx1 = poly.cx1.AddFixed(poly.dr0)
			} else {
				poly.cx0 = poly.cx0.AddFixed(poly.dl1)
				poly.cx1 = poly.cx1.AddFixed(poly.dr1)
			}
		}

		y++
	}
}

func (e3d *HwEngine3d) BeginFrame() {
	// Acquire the frame lock, we will begin drawing now
	e3d.frameLock.Lock()
}

func (e3d *HwEngine3d) EndFrame() {
	// Release the frame lock, drawing is finished
	e3d.frameLock.Unlock()
}