// Package retarget redimensiona páginas de mangá/HQ preservando as regiões de
// saliência (personagens/objetos/linhas estruturais), em vez de remover
// costuras como o seam carving.
//
// Princípio (pesquisa de mangá): linha e trama não devem ser redimensionadas
// juntas com um operador destrutivo. Quando o aspect ratio NÃO muda, basta
// interpolação de alta qualidade (Catmull-Rom). Quando o aspect ratio muda
// (retargeting real), as regiões de saliência são escaladas uniformemente
// (aspecto preservado, sem shear) e o fundo é reamostrado suavemente para
// absorver a diferença de tamanho.
package retarget

import (
	"image"
	"image/color"
	"math"

	"golang.org/x/image/draw"
)

// SameAspect reporta se dois tamanhos têm o mesmo aspect ratio (tolerância de
// 1%). Evita retargeting desnecessário quando apenas a resolução muda.
func SameAspect(w1, h1, w2, h2 int) bool {
	if h1 == 0 || h2 == 0 {
		return true
	}
	a1 := float64(w1) / float64(h1)
	a2 := float64(w2) / float64(h2)
	return math.Abs(a1-a2)/a1 < 0.01
}

// Fit redimensiona src para dstW x dstH. mask (gray B&W, branco = protegido)
// marca as regiões de saliência a preservar; se não for fornecido no tamanho
// de src, é reescalado internamente. Se mask for nil ou os aspect ratios
// coincidirem, aplica apenas interpolação de alta qualidade.
func Fit(src image.Image, mask image.Image, dstW, dstH int) image.Image {
	if dstW <= 0 || dstH <= 0 {
		return src
	}
	b := src.Bounds()
	srcW, srcH := b.Dx(), b.Dy()
	if srcW == dstW && srcH == dstH {
		return src
	}

	// Se a máscara não vier no tamanho de src (ex.: página ampliada), alinha.
	if mask != nil && (mask.Bounds().Dx() != srcW || mask.Bounds().Dy() != srcH) {
		mask = resize(mask, srcW, srcH)
	}

	// Mesmo aspect ratio (ou sem máscara): só interpolação de qualidade.
	if mask == nil || SameAspect(srcW, srcH, dstW, dstH) {
		return resize(src, dstW, dstH)
	}

	colSal, rowSal := protectAxes(mask, srcW, srcH)
	xMap := axisMap(srcW, dstW, colSal)
	yMap := axisMap(srcH, dstH, rowSal)
	return warp(src, dstW, dstH, xMap, yMap)
}

// protectAxes identifica os componentes conexos da máscara e devolve, por eixo,
// quais posições (colunas/linhas) pertencem ao bounding box de algum componente
// significativo. Proteger o bounding box preserva a extensão do objeto sem
// depender de fração de pixels por coluna (que falha para objetos pequenos ou
// traços finos).
func protectAxes(mask image.Image, w, h int) (protX, protY []bool) {
	protX = make([]bool, w)
	protY = make([]bool, h)

	b := mask.Bounds()
	protected := make([]bool, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := color.GrayModel.Convert(mask.At(b.Min.X+x, b.Min.Y+y)).(color.Gray)
			protected[y*w+x] = c.Y > 0
		}
	}

	// Componentes conexos (4-vizinhança); protege o bounding box dos que têm
	// área >= minArea. minArea baixo pois a máscara já vem filtrada a montante.
	const minArea = 16
	visited := make([]bool, w*h)
	for start := 0; start < w*h; start++ {
		if visited[start] || !protected[start] {
			continue
		}
		stack := []int{start}
		visited[start] = true
		var comp []int
		for len(stack) > 0 {
			i := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			comp = append(comp, i)
			x, y := i%w, i/w
			for _, nb := range [4][2]int{{x - 1, y}, {x + 1, y}, {x, y - 1}, {x, y + 1}} {
				nx, ny := nb[0], nb[1]
				if nx < 0 || nx >= w || ny < 0 || ny >= h {
					continue
				}
				j := ny*w + nx
				if visited[j] || !protected[j] {
					continue
				}
				visited[j] = true
				stack = append(stack, j)
			}
		}
		if len(comp) < minArea {
			continue
		}
		x0, y0, x1, y1 := w, h, -1, -1
		for _, i := range comp {
			x, y := i%w, i/w
			if x < x0 {
				x0 = x
			}
			if x > x1 {
				x1 = x
			}
			if y < y0 {
				y0 = y
			}
			if y > y1 {
				y1 = y
			}
		}
		for x := x0; x <= x1; x++ {
			protX[x] = true
		}
		for y := y0; y <= y1; y++ {
			protY[y] = true
		}
	}
	return protX, protY
}

type axisSeg struct {
	s0, s1 float64 // faixa de origem (coordenadas de src)
	t0, t1 float64 // faixa de destino (coordenadas de target)
	prot   bool
}

// axisMap constrói o mapeamento fonte->destino para um eixo. Posições marcadas
// como protegidas (bounding box de componentes de saliência) são mapeadas em
// escala 1 (preservando o aspecto); o fundo absorve a diferença de tamanho com
// reamostragem suave. Devolve, para cada pixel de destino t, a coordenada de
// origem (float).
func axisMap(srcN, dstN int, prot []bool) []float64 {
	out := make([]float64, dstN)

	// Intervalos contíguos protegidos [s, e).
	var protInt [][2]int
	i := 0
	for i < srcN {
		if prot[i] {
			s := i
			for i < srcN && prot[i] {
				i++
			}
			protInt = append(protInt, [2]int{s, i})
		} else {
			i++
		}
	}

	protTotal := 0
	for _, p := range protInt {
		protTotal += p[1] - p[0]
	}

	// Se as regiões protegidas sozinhas excedem o destino, não há fundo para
	// absorver: recorre à interpolação uniforme.
	bgTarget := dstN - protTotal
	if bgTarget < 0 || protTotal >= srcN {
		return uniformMap(srcN, dstN)
	}

	bgSrc := srcN - protTotal
	bgScale := float64(bgTarget) / float64(bgSrc)

	// Monta segmentos na ordem de origem com flag explícita de protegido.
	var segs []axisSeg
	cursor := 0
	for _, p := range protInt {
		if cursor < p[0] {
			segs = append(segs, axisSeg{float64(cursor), float64(p[0]), 0, 0, false})
		}
		segs = append(segs, axisSeg{float64(p[0]), float64(p[1]), 0, 0, true})
		cursor = p[1]
	}
	if cursor < srcN {
		segs = append(segs, axisSeg{float64(cursor), float64(srcN), 0, 0, false})
	}

	// Escala exata: protegidos em 1, fundo em bgScale; o total fecha dstN.
	t := 0.0
	for k := range segs {
		segs[k].t0 = t
		srcLen := segs[k].s1 - segs[k].s0
		tgtLen := srcLen
		if !segs[k].prot {
			tgtLen = srcLen * bgScale
		}
		segs[k].t1 = t + tgtLen
		t = segs[k].t1
	}
	if len(segs) == 0 {
		return uniformMap(srcN, dstN)
	}

	sampleAt := func(tc float64) float64 {
		if tc <= 0 {
			return 0
		}
		if tc >= float64(dstN) {
			return float64(srcN)
		}
		for _, sg := range segs {
			if tc >= sg.t0 && tc <= sg.t1 {
				if sg.t1 == sg.t0 {
					return sg.s0
				}
				ratio := (tc - sg.t0) / (sg.t1 - sg.t0)
				x := sg.s0 + ratio*(sg.s1-sg.s0)
				if x < 0 {
					x = 0
				}
				if x > float64(srcN) {
					x = float64(srcN)
				}
				return x
			}
		}
		return float64(srcN)
	}

	for t := 0; t < dstN; t++ {
		out[t] = sampleAt(float64(t) + 0.5)
	}
	return out
}

// uniformMap é um mapeamento linear (interpolação uniforme) por eixo.
func uniformMap(srcN, dstN int) []float64 {
	out := make([]float64, dstN)
	scale := float64(srcN) / float64(dstN)
	for t := 0; t < dstN; t++ {
		out[t] = (float64(t) + 0.5) * scale
	}
	return out
}

// warp reamostra src para dstW x dstH usando os mapeamentos por eixo xMap/yMap
// com interpolação Catmull-Rom (bicúbica).
func warp(src image.Image, dstW, dstH int, xMap, yMap []float64) *image.RGBA {
	work := toRGBA(src)
	b := work.Bounds()
	sw, sh := b.Dx(), b.Dy()

	dst := image.NewRGBA(image.Rect(0, 0, dstW, dstH))
	for y := 0; y < dstH; y++ {
		fy := yMap[y]
		for x := 0; x < dstW; x++ {
			dst.Set(x, y, sampleCatmullRom(work, sw, sh, xMap[x], fy))
		}
	}
	return dst
}

// toRGBA converte uma imagem para *image.RGBA para acesso direto aos pixels.
func toRGBA(img image.Image) *image.RGBA {
	if r, ok := img.(*image.RGBA); ok {
		return r
	}
	b := img.Bounds()
	out := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(out, out.Bounds(), img, b.Min, draw.Src)
	return out
}

// resize redimensiona com interpolação Catmull-Rom de alta qualidade.
func resize(src image.Image, w, h int) *image.RGBA {
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, src.Bounds(), draw.Over, nil)
	return dst
}

// sampleCatmullRom amostra um pixel em (fx, fy) com bicúbica (Catmull-Rom).
func sampleCatmullRom(img *image.RGBA, w, h int, fx, fy float64) color.RGBA {
	x0 := int(math.Floor(fx))
	y0 := int(math.Floor(fy))
	tx := fx - float64(x0)
	ty := fy - float64(y0)

	// Índices com clamp para as bordas.
	idx := make([]int, 4)
	for i := 0; i < 4; i++ {
		xi := x0 - 1 + i
		if xi < 0 {
			xi = 0
		}
		if xi > w-1 {
			xi = w - 1
		}
		idx[i] = xi
	}
	idy := make([]int, 4)
	for i := 0; i < 4; i++ {
		yi := y0 - 1 + i
		if yi < 0 {
			yi = 0
		}
		if yi > h-1 {
			yi = h - 1
		}
		idy[i] = yi
	}

	rows := make([]uint8, 4*4*4) // [y][x][channel]
	for j := 0; j < 4; j++ {
		row := img.Pix[idy[j]*img.Stride : idy[j]*img.Stride+w*4]
		for i := 0; i < 4; i++ {
			off := idx[i] * 4
			rows[(j*4+i)*4+0] = row[off+0]
			rows[(j*4+i)*4+1] = row[off+1]
			rows[(j*4+i)*4+2] = row[off+2]
			rows[(j*4+i)*4+3] = row[off+3]
		}
	}

	var c color.RGBA
	for ch := 0; ch < 4; ch++ {
		// Primeiro interpola sobre x em cada linha, depois sobre y.
		col := make([]float64, 4)
		for j := 0; j < 4; j++ {
			col[j] = catmull(
				float64(rows[(j*4+0)*4+ch]),
				float64(rows[(j*4+1)*4+ch]),
				float64(rows[(j*4+2)*4+ch]),
				float64(rows[(j*4+3)*4+ch]),
				tx,
			)
		}
		v := catmull(col[0], col[1], col[2], col[3], ty)
		if v < 0 {
			v = 0
		}
		if v > 255 {
			v = 255
		}
		switch ch {
		case 0:
			c.R = uint8(v)
		case 1:
			c.G = uint8(v)
		case 2:
			c.B = uint8(v)
		case 3:
			c.A = uint8(v)
		}
	}
	return c
}

// catmull avalia o spline Catmull-Rom em t a partir de quatro amostras.
func catmull(p0, p1, p2, p3, t float64) float64 {
	return 0.5 * (2*p1 + (-p0+p2)*t + (2*p0-5*p1+4*p2-p3)*t*t + (-p0+3*p1-3*p2+p3)*t*t*t)
}
