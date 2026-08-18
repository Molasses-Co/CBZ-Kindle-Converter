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

	"golang.org/x/image/draw"
)

var forward = true

var (
	levelsEnabled = true
	blackPoint    = float32(30)
	whitePoint    = float32(225)
)

var (
	shieldEnabled  = true
	shieldStrength = float32(4)
)

const (
	protectCost     float32 = 1e7
	maxCost         float32 = 1e20
	edgeWeight      float32 = 8
	centerCost      float32 = 4
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
//
// When the mask marks connected protected components, a hybrid strategy is used:
// the protected objects are extracted and rescaled with uniform (bicubic)
// interpolation, while seam carving runs only on the background. This
// guarantees a long diagonal protected object (e.g. a sickle handle) is never
// sheared by seams alternating which side they route around.
func ResizeWithMask(src, mask image.Image, width, height int) image.Image {
	if src == nil || width <= 0 || height <= 0 {
		return src
	}
	p := newPlanes(src, mask)
	if p.w == 0 || p.h == 0 {
		return src
	}
	if comps := protectedComponents(p.protect, p.w, p.h, protectCrossThr); len(comps) > 0 {
		return hybridResize(p, src, width, height, comps)
	}
	return carvePlanes(p, width, height)
}

// carvePlanes runs the one-seam-at-a-time seam carving on the given planes and
// returns the resized image (applying the levels filter at the end).
func carvePlanes(p *planes, width, height int) image.Image {
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

// component is the bounding box of a connected protected region.
type component struct{ x0, y0, x1, y1 int }

// protectedComponents flood-fills connected protected components (4-connectivity)
// and returns their bounding boxes.
func protectedComponents(protect []float32, w, h int, thr float32) []component {
	visited := make([]bool, w*h)
	var comps []component
	for start := 0; start < w*h; start++ {
		if visited[start] || protect[start] < thr {
			continue
		}
		stack := []int{start}
		visited[start] = true
		minX, maxX := start%w, start%w
		minY, maxY := start/w, start/w
		for len(stack) > 0 {
			i := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			x, y := i%w, i/w
			if x < minX {
				minX = x
			}
			if x > maxX {
				maxX = x
			}
			if y < minY {
				minY = y
			}
			if y > maxY {
				maxY = y
			}
			for _, d := range [4]int{-1, 1, -w, w} {
				j := i + d
				if j < 0 || j >= w*h {
					continue
				}
				if (i%w == 0 && d == -1) || (i%w == w-1 && d == 1) {
					continue
				}
				if visited[j] || protect[j] < thr {
					continue
				}
				visited[j] = true
				stack = append(stack, j)
			}
		}
		comps = append(comps, component{minX, minY, maxX + 1, maxY + 1})
	}
	return comps
}

// backgroundPlanes returns a copy of p with protected pixels replaced by the
// average background color, so seam carving treats the object region as
// structureless background.
func (p *planes) backgroundPlanes(thr float32) *planes {
	ar, ag, ab := p.averageBackground(thr)
	q := &planes{w: p.w, h: p.h}
	n := p.w * p.h
	q.gray, q.protect = make([]float32, n), make([]float32, n)
	q.r, q.g, q.b, q.a = make([]uint8, n), make([]uint8, n), make([]uint8, n), make([]uint8, n)
	for i := 0; i < n; i++ {
		q.r[i], q.g[i], q.b[i], q.a[i] = p.r[i], p.g[i], p.b[i], p.a[i]
		if p.protect[i] >= thr {
			q.r[i], q.g[i], q.b[i] = ar, ag, ab
		}
		q.gray[i] = luma(q.r[i], q.g[i], q.b[i])
	}
	return q
}

// averageBackground returns the mean RGB of the non-protected pixels.
func (p *planes) averageBackground(thr float32) (uint8, uint8, uint8) {
	var r, g, b, n uint64
	for i := range p.r {
		if p.protect[i] >= thr {
			continue
		}
		r += uint64(p.r[i])
		g += uint64(p.g[i])
		b += uint64(p.b[i])
		n++
	}
	if n == 0 {
		return 255, 255, 255
	}
	return uint8(r / n), uint8(g / n), uint8(b / n)
}

// hybridResize carves only the background to the target size, then rescales each
// protected object uniformly (bicubic) and composites it back at the position
// scaled proportionally to the target.
func hybridResize(p *planes, src image.Image, width, height int, comps []component) image.Image {
	srcW, srcH := p.w, p.h
	bg := carvePlanes(p.backgroundPlanes(protectCrossThr), width, height).(*image.RGBA)

	for _, c := range comps {
		cw, ch := c.x1-c.x0, c.y1-c.y0
		if cw <= 0 || ch <= 0 {
			continue
		}
		dw := cw * width / srcW
		dh := ch * height / srcH
		if dw < 1 {
			dw = 1
		}
		if dh < 1 {
			dh = 1
		}
		obj := crop(src, c.x0, c.y0, c.x1, c.y1)
		scaled := image.NewRGBA(image.Rect(0, 0, dw, dh))
		draw.CatmullRom.Scale(scaled, scaled.Bounds(), obj, obj.Bounds(), draw.Over, nil)
		if levelsEnabled {
			applyLevelsRGBA(scaled, blackPoint, whitePoint)
		}
		px := c.x0 * width / srcW
		py := c.y0 * height / srcH
		r := image.Rect(px, py, px+dw, py+dh)
		sp := image.Point{0, 0}
		if r.Min.X < 0 {
			sp.X = -r.Min.X
			r.Min.X = 0
		}
		if r.Min.Y < 0 {
			sp.Y = -r.Min.Y
			r.Min.Y = 0
		}
		if r.Max.X > bg.Bounds().Max.X {
			r.Max.X = bg.Bounds().Max.X
		}
		if r.Max.Y > bg.Bounds().Max.Y {
			r.Max.Y = bg.Bounds().Max.Y
		}
		if !r.Empty() {
			draw.Draw(bg, r, scaled, sp, draw.Over)
		}
	}
	return bg
}

func crop(src image.Image, x0, y0, x1, y1 int) *image.RGBA {
	b := src.Bounds()
	r := image.Rect(b.Min.X+x0, b.Min.Y+y0, b.Min.X+x1, b.Min.Y+y1)
	dst := image.NewRGBA(image.Rect(0, 0, x1-x0, y1-y0))
	draw.Draw(dst, dst.Bounds(), src, r.Min, draw.Src)
	return dst
}

func applyLevelsRGBA(img *image.RGBA, black, white float32) {
	if white <= black {
		return
	}
	rng := white - black
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i] = level(img.Pix[i], black, rng)
		img.Pix[i+1] = level(img.Pix[i+1], black, rng)
		img.Pix[i+2] = level(img.Pix[i+2], black, rng)
	}
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
// protected subject whenever an unobstructed, finite-cost path exists.
func (p *planes) findSeam() []int32 {
	energy := p.energy()
	seam := p.bestSeam(energy)
	if !p.crossesProtection(seam) {
		return seam
	}

	hard := p.energy()
	for i := range hard {
		if p.protect[i] >= protectCrossThr {
			hard[i] = maxCost
		}
	}
	if alt, finite := p.bestSeamCost(hard); finite && !p.crossesProtection(alt) {
		return alt
	}

	// Se não existe caminho livre de proteção, prefira a seam original de menor
	// custo em vez de uma rota arbitrária saturada.
	return seam
}

func (p *planes) bestSeam(energy []float32) []int32 {
	if forward {
		return forwardSeam(p.gray, energy, p.w, p.h)
	}
	return backwardSeam(energy, p.w, p.h)
}

// bestSeamCost returns the best seam and whether it stays finite throughout
// (never touches a pixel hard-forbidden with maxCost).
func (p *planes) bestSeamCost(energy []float32) ([]int32, bool) {
	seam := p.bestSeam(energy)
	for y, sx := range seam {
		if energy[y*p.w+int(sx)] >= maxCost {
			return nil, false
		}
	}
	return seam, true
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

	// Sentinels: transitions that would leave the image are invalid, never
	// clamped. A boundary parent keeps cost maxCost so it is never preferred
	// while a finite path exists.
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
			best, par := satAdd(prev[x], cl), int32(x-1)
			if v := satAdd(prev[x+1], mid); v < best {
				best, par = v, int32(x)
			}
			if v := satAdd(prev[x+2], cr); v < best {
				best, par = v, int32(x+1)
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
			best, par := prev[x], int32(x-1)
			if prev[x+1] < best {
				best, par = prev[x+1], int32(x)
			}
			if prev[x+2] < best {
				best, par = prev[x+2], int32(x+1)
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
		cur := int(seam[y])
		// First row stores no parent.
		if y == 1 {
			seam[y-1] = int32(cur)
			continue
		}
		p := parent[y*w+cur]
		// Safety net: a parent outside [0,w) means a boundary sentinel leaked
		// in; fall back to staying in the same column rather than an invalid one.
		if p < 0 || p >= int32(w) {
			p = int32(cur)
		}
		seam[y-1] = p
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
