// Package seamcarve implements content-aware image resizing in pure Go.
//
// This version favors preservation of illustrated objects and thin line art over
// throughput: seams are removed/inserted one at a time and energy is recomputed
// after every edit. The energy combines forward disruption, Scharr structure,
// locally dilated edges, a soft protection mask, and an optional center prior.
package seamcarve

import (
	"image"
	"image/color"
	"math"
	"runtime"
	"sync"
)

var forward = true

var (
	levelsEnabled = true
	blackPoint    = float32(30)
	whitePoint    = float32(225)
)

var (
	shieldEnabled  = true
	shieldStrength = float32(10)
)

const (
	protectCost     float32 = 1e7
	maxCost         float32 = 1e20
	edgeWeight      float32 = 8
	centerCost      float32 = 24
	orientWeight    float32 = 4
	protectCrossThr float32 = 0.5
	maxWorkers              = 8
)

// UseForwardEnergy selects forward (true) or backward (false) seam energy.
func UseForwardEnergy(v bool) { forward = v }

func SetLevels(black, white float32, enabled bool) {
	blackPoint, whitePoint, levelsEnabled = black, white, enabled
}

func SetCenterShield(strength float64, enabled bool) {
	if strength < 0 {
		strength = 0
	}
	shieldStrength, shieldEnabled = float32(strength), enabled
}

// Resize resizes src using automatic structure protection. For important
// subjects such as weapons, faces, lettering, or hands, prefer ResizeWithMask.
func Resize(src image.Image, width, height int) image.Image {
	return ResizeWithMask(src, nil, width, height)
}

// ResizeWithMask accepts a soft protection mask. Black means no additional
// protection and white means strongly protected. Mask bounds need not start at
// (0,0); masks of another size are sampled proportionally.
func ResizeWithMask(src, mask image.Image, width, height int) image.Image {
	if src == nil || width <= 0 || height <= 0 {
		return src
	}
	p := newPlanes(src, mask)
	if p.w == 0 || p.h == 0 {
		return src
	}
	p.resizeWidth(width)
	p.transpose()
	p.resizeWidth(height)
	p.transpose()
	if levelsEnabled {
		applyLevels(p.r, p.g, p.b, blackPoint, whitePoint)
	}
	return p.image()
}

type planes struct {
	gray, protect []float32
	r, g, b, a    []uint8
	w, h          int
}

func newPlanes(img image.Image, mask image.Image) *planes {
	b := img.Bounds()
	p := &planes{w: b.Dx(), h: b.Dy()}
	n := p.w * p.h
	p.gray = make([]float32, n)
	p.protect = make([]float32, n)
	p.r, p.g, p.b, p.a = make([]uint8, n), make([]uint8, n), make([]uint8, n), make([]uint8, n)
	for y := 0; y < p.h; y++ {
		for x := 0; x < p.w; x++ {
			i := y*p.w + x
			c := color.RGBAModel.Convert(img.At(b.Min.X+x, b.Min.Y+y)).(color.RGBA)
			p.r[i], p.g[i], p.b[i], p.a[i] = c.R, c.G, c.B, c.A
			p.gray[i] = luma(c.R, c.G, c.B)
			if mask != nil {
				p.protect[i] = sampleMask(mask, x, y, p.w, p.h)
			}
		}
	}
	return p
}

func sampleMask(mask image.Image, x, y, dstW, dstH int) float32 {
	mb := mask.Bounds()
	mw, mh := mb.Dx(), mb.Dy()
	if mw <= 0 || mh <= 0 {
		return 0
	}
	mx := mb.Min.X + x*mw/dstW
	my := mb.Min.Y + y*mh/dstH
	if mx >= mb.Max.X {
		mx = mb.Max.X - 1
	}
	if my >= mb.Max.Y {
		my = mb.Max.Y - 1
	}
	v := color.GrayModel.Convert(mask.At(mx, my)).(color.Gray).Y
	return float32(v) / 255
}

func (p *planes) resizeWidth(target int) {
	if target <= 0 || target == p.w {
		return
	}
	if target < p.w {
		if p.w <= 1 {
			return
		}
		for p.w > target {
			seam := p.findSeam()
			p.remove(seam)
			if p.w <= 1 {
				break
			}
		}
		return
	}
	for p.w < target {
		seam := p.findSeam()
		p.insert(seam)
	}
}

// findSeam recalculates all structure costs. This deliberate one-seam policy
// prevents a batch of seams from collectively cutting through a thin object.
// If the lowest-cost seam crosses a strongly protected region, a second pass
// that hard-forbids protected pixels is attempted so the cut routes around the
// protected subject whenever an unobstructed path exists.
func (p *planes) findSeam() []int32 {
	energy := p.energy()
	var seam []int32
	if forward {
		seam = forwardSeam(p.gray, energy, p.w, p.h)
	} else {
		seam = backwardSeam(energy, p.w, p.h)
	}
	if p.crossesProtection(seam) {
		if alt := p.avoidProtectionSeam(); len(alt) > 0 && !p.crossesProtection(alt) {
			seam = alt
		}
	}
	return seam
}

// crossesProtection reports whether the seam passes over any strongly protected
// pixel.
func (p *planes) crossesProtection(seam []int32) bool {
	for y, sx := range seam {
		if p.protect[y*p.w+int(sx)] >= protectCrossThr {
			return true
		}
	}
	return false
}

// avoidProtectionSeam computes a seam treating protected pixels as hard-forbidden
// (cost = maxCost). It returns nil if no seam avoids them (i.e. every path must
// cross the protected region).
func (p *planes) avoidProtectionSeam() []int32 {
	energy := p.energy()
	for i := range energy {
		if p.protect[i] >= protectCrossThr {
			energy[i] = maxCost
		}
	}
	var seam []int32
	if forward {
		seam = forwardSeam(p.gray, energy, p.w, p.h)
	} else {
		seam = backwardSeam(energy, p.w, p.h)
	}
	if p.crossesProtection(seam) {
		return nil
	}
	return seam
}

// energy returns an additive, finite cost map. Scharr magnitude captures line
// art; local 3x3 dilation protects the white interior immediately adjacent to a
// contour; the user mask and center prior are independent additive terms.
func (p *planes) energy() []float32 {
	n := p.w * p.h
	gx := make([]float32, n)
	gy := make([]float32, n)
	parallelRows(p.h, func(y0, y1 int) {
		for y := y0; y < y1; y++ {
			for x := 0; x < p.w; x++ {
				i := y*p.w + x
				gx[i], gy[i] = scharrAt(p.gray, p.w, p.h, x, y)
			}
		}
	})
	out := make([]float32, n)
	parallelRows(p.h, func(y0, y1 int) {
		for y := y0; y < y1; y++ {
			for x := 0; x < p.w; x++ {
				i := y*p.w + x
				// Local 3x3 dilation: protects the region immediately around a
				// contour (e.g. white interiors bordered by black line art).
				local := float32(0)
				for dy := -1; dy <= 1; dy++ {
					yy := reflectIndex(y+dy, p.h)
					for dx := -1; dx <= 1; dx++ {
						mag := abs32(gx[yy*p.w+reflectIndex(x+dx, p.w)]) + abs32(gy[yy*p.w+reflectIndex(x+dx, p.w)])
						if mag > local {
							local = mag
						}
					}
				}
				mag := abs32(gx[i]) + abs32(gy[i])
				v := mag + edgeWeight*local
				// Orientation term: penalize cutting across vertical edges (a
				// seam column that passes through a vertical edge severs it).
				v += orientWeight * abs32(gx[i])
				v += p.protect[i] * protectCost
				v += centerPrior(p.w, p.h, x, y) * centerCost
				out[i] = clampCost(v)
			}
		}
	})
	return out
}

func parallelRows(h int, fn func(y0, y1 int)) {
	workers := runtime.GOMAXPROCS(0)
	if workers > maxWorkers {
		workers = maxWorkers
	}
	if workers < 1 {
		workers = 1
	}
	if h < workers*16 {
		workers = 1
	}
	if workers == 1 {
		fn(0, h)
		return
	}
	step := (h + workers - 1) / workers
	var wg sync.WaitGroup
	for y := 0; y < h; y += step {
		y0, y1 := y, y+step
		if y1 > h {
			y1 = h
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			fn(y0, y1)
		}()
	}
	wg.Wait()
}

func forwardSeam(gray, salience []float32, w, h int) []int32 {
	parent := make([]int32, w*h)
	prev, next := make([]float32, w+2), make([]float32, w+2)
	prev[0], prev[w+1] = maxCost, maxCost
	for x := 0; x < w; x++ {
		left, right := gray[x], gray[x]
		if x > 0 {
			left = gray[x-1]
		}
		if x+1 < w {
			right = gray[x+1]
		}
		prev[x+1] = clampCost(abs32(right-left) + salience[x])
	}
	for y := 1; y < h; y++ {
		next[0], next[w+1] = maxCost, maxCost
		for x := 0; x < w; x++ {
			i := y*w + x
			left, right := gray[i], gray[i]
			if x > 0 {
				left = gray[i-1]
			}
			if x+1 < w {
				right = gray[i+1]
			}
			up := gray[i-w]
			mid := abs32(right-left) + salience[i]
			cl := mid + abs32(up-left)
			cr := mid + abs32(up-right)
			best, par := satAdd(prev[x], cl), int32(clampIdx(x-1, w))
			if v := satAdd(prev[x+1], mid); v < best {
				best, par = v, int32(x)
			}
			if v := satAdd(prev[x+2], cr); v < best {
				best, par = v, int32(clampIdx(x+1, w))
			}
			next[x+1], parent[i] = best, par
		}
		prev, next = next, prev
	}
	end := 0
	for x := 1; x < w; x++ {
		if prev[x+1] < prev[end+1] {
			end = x
		}
	}
	return trace(parent, w, h, end)
}

func backwardSeam(energy []float32, w, h int) []int32 {
	parent := make([]int32, w*h)
	prev, next := make([]float32, w+2), make([]float32, w+2)
	prev[0], prev[w+1] = maxCost, maxCost
	copy(prev[1:w+1], energy[:w])
	for y := 1; y < h; y++ {
		next[0], next[w+1] = maxCost, maxCost
		for x := 0; x < w; x++ {
			best, par := prev[x], int32(clampIdx(x-1, w))
			if prev[x+1] < best {
				best, par = prev[x+1], int32(x)
			}
			if prev[x+2] < best {
				best, par = prev[x+2], int32(clampIdx(x+1, w))
			}
			next[x+1], parent[y*w+x] = satAdd(best, energy[y*w+x]), par
		}
		prev, next = next, prev
	}
	end := 0
	for x := 1; x < w; x++ {
		if prev[x+1] < prev[end+1] {
			end = x
		}
	}
	return trace(parent, w, h, end)
}

func trace(parent []int32, w, h, end int) []int32 {
	seam := make([]int32, h)
	seam[h-1] = int32(end)
	for y := h - 1; y > 0; y-- {
		seam[y-1] = parent[y*w+int(seam[y])]
	}
	return seam
}

func (p *planes) remove(seam []int32) {
	if p.w <= 1 {
		return
	}
	nw := p.w - 1
	q := &planes{w: nw, h: p.h}
	n := nw * p.h
	q.gray, q.protect = make([]float32, n), make([]float32, n)
	q.r, q.g, q.b, q.a = make([]uint8, n), make([]uint8, n), make([]uint8, n), make([]uint8, n)
	for y, sx32 := range seam {
		sx, src, dst := int(sx32), y*p.w, y*nw
		copy(q.gray[dst:dst+sx], p.gray[src:src+sx])
		copy(q.gray[dst+sx:dst+nw], p.gray[src+sx+1:src+p.w])
		copy(q.protect[dst:dst+sx], p.protect[src:src+sx])
		copy(q.protect[dst+sx:dst+nw], p.protect[src+sx+1:src+p.w])
		copy(q.r[dst:dst+sx], p.r[src:src+sx])
		copy(q.r[dst+sx:dst+nw], p.r[src+sx+1:src+p.w])
		copy(q.g[dst:dst+sx], p.g[src:src+sx])
		copy(q.g[dst+sx:dst+nw], p.g[src+sx+1:src+p.w])
		copy(q.b[dst:dst+sx], p.b[src:src+sx])
		copy(q.b[dst+sx:dst+nw], p.b[src+sx+1:src+p.w])
		copy(q.a[dst:dst+sx], p.a[src:src+sx])
		copy(q.a[dst+sx:dst+nw], p.a[src+sx+1:src+p.w])
	}
	*p = *q
}

func (p *planes) insert(seam []int32) {
	nw := p.w + 1
	q := &planes{w: nw, h: p.h}
	n := nw * p.h
	q.gray, q.protect = make([]float32, n), make([]float32, n)
	q.r, q.g, q.b, q.a = make([]uint8, n), make([]uint8, n), make([]uint8, n), make([]uint8, n)
	for y, sx32 := range seam {
		sx, src, dst := int(sx32), y*p.w, y*nw
		for x := 0; x < p.w; x++ {
			if x == sx {
				// Edge-guided interpolation: blend with the neighbor of smaller
				// gradient to avoid blurring/duplicating strokes.
				nL, nR := x-1, x+1
				if nL < 0 {
					nL = x
				}
				if nR >= p.w {
					nR = x
				}
				neighbor := nL
				if abs32(p.gray[src+nR]-p.gray[src+x]) < abs32(p.gray[src+nL]-p.gray[src+x]) {
					neighbor = nR
				}
				q.r[dst], q.g[dst], q.b[dst], q.a[dst] = avg8(p.r[src+x], p.r[src+neighbor]), avg8(p.g[src+x], p.g[src+neighbor]), avg8(p.b[src+x], p.b[src+neighbor]), avg8(p.a[src+x], p.a[src+neighbor])
				q.gray[dst] = luma(q.r[dst], q.g[dst], q.b[dst])
				q.protect[dst] = max32(p.protect[src+x], p.protect[src+neighbor])
				dst++
			}
			q.r[dst], q.g[dst], q.b[dst], q.a[dst] = p.r[src+x], p.g[src+x], p.b[src+x], p.a[src+x]
			q.gray[dst], q.protect[dst] = p.gray[src+x], p.protect[src+x]
			dst++
		}
	}
	*p = *q
}

func (p *planes) transpose() {
	q := &planes{w: p.h, h: p.w}
	n := p.w * p.h
	q.gray, q.protect = make([]float32, n), make([]float32, n)
	q.r, q.g, q.b, q.a = make([]uint8, n), make([]uint8, n), make([]uint8, n), make([]uint8, n)
	for y := 0; y < p.h; y++ {
		for x := 0; x < p.w; x++ {
			s, d := y*p.w+x, x*q.w+y
			q.gray[d], q.protect[d] = p.gray[s], p.protect[s]
			q.r[d], q.g[d], q.b[d], q.a[d] = p.r[s], p.g[s], p.b[s], p.a[s]
		}
	}
	*p = *q
}

func (p *planes) image() image.Image {
	dst := image.NewRGBA(image.Rect(0, 0, p.w, p.h))
	for i := range p.r {
		j := i * 4
		dst.Pix[j], dst.Pix[j+1], dst.Pix[j+2], dst.Pix[j+3] = p.r[i], p.g[i], p.b[i], p.a[i]
	}
	return dst
}

// scharrAt returns the Scharr gradient components (Gx, Gy) at (x, y). Scharr
// responds more isotropically than Sobel, preserving thin diagonal strokes such
// as the handle of a sickle.
func scharrAt(gray []float32, w, h, x, y int) (float32, float32) {
	x0, x1 := reflectIndex(x-1, w), reflectIndex(x+1, w)
	y0, y1 := reflectIndex(y-1, h), reflectIndex(y+1, h)
	tl, tc, tr := gray[y0*w+x0], gray[y0*w+x], gray[y0*w+x1]
	ml, mr := gray[y*w+x0], gray[y*w+x1]
	bl, bc, br := gray[y1*w+x0], gray[y1*w+x], gray[y1*w+x1]
	gx := -3*tl + 3*tr - 10*ml + 10*mr - 3*bl + 3*br
	gy := -3*tl - 10*tc - 3*tr + 3*bl + 10*bc + 3*br
	return gx, gy
}

func centerPrior(w, h, x, y int) float32 {
	if !shieldEnabled || shieldStrength <= 0 {
		return 0
	}
	cx, cy := float32(w-1)/2, float32(h-1)/2
	dx, dy := float32(x)-cx, float32(y)-cy
	maxD := float32(math.Hypot(float64(cx), float64(cy)))
	if maxD == 0 {
		return shieldStrength
	}
	d := float32(math.Hypot(float64(dx), float64(dy))) / maxD
	if d > 1 {
		d = 1
	}
	return (1 - d) * shieldStrength
}

func reflectIndex(i, n int) int {
	if n <= 1 {
		return 0
	}
	if i < 0 {
		return -i
	}
	if i >= n {
		return 2*n - 2 - i
	}
	return i
}
func luma(r, g, b uint8) float32 {
	return .2126*float32(r) + .7152*float32(g) + .0722*float32(b)
}
func abs32(v float32) float32 {
	if v < 0 {
		return -v
	}
	return v
}
func clampIdx(i, w int) int {
	if i < 0 {
		return 0
	}
	if i >= w {
		return w - 1
	}
	return i
}
func max32(a, b float32) float32 {
	if a > b {
		return a
	}
	return b
}
func avg8(a, b uint8) uint8 {
	return uint8((uint16(a) + uint16(b)) / 2)
}
func clampCost(v float32) float32 {
	if v > maxCost {
		return maxCost
	}
	return v
}
func satAdd(a, b float32) float32 {
	if a >= maxCost-b {
		return maxCost
	}
	return a + b
}

func applyLevels(r, g, b []uint8, black, white float32) {
	if white <= black {
		return
	}
	rng := white - black
	for i := range r {
		r[i] = level(r[i], black, rng)
		g[i] = level(g[i], black, rng)
		b[i] = level(b[i], black, rng)
	}
}
func level(v uint8, min, rng float32) uint8 {
	f := float32(v)
	if f <= min {
		return 0
	}
	if f >= min+rng {
		return 255
	}
	return uint8((f - min) * 255 / rng)
}
