// Package seamcarve implementa seam carving em Go puro, usado para redimensionar
// (reduzir ou expandir) uma imagem preservando o conteúdo importante via remoção
// ou inserção de "costuras" (seams) de menor energia.
//
// É um port do algoritmo clássico (Avidan & Shamir) com energia forward
// (melhor qualidade) e suporte a energia backward (gradiente Sobel). A redução
// é feita em lotes (encontra N costuras de uma vez e remove todas juntas), o
// que é bem mais rápido do que remover uma a uma.
package seamcarve

import (
	"image"
	"image/color"
	"math"
)

// forward define o tipo de energia usada: true = forward (default), false = backward.
var forward = true

// Parâmetros do filtro de níveis (contraste) aplicado ao final do Resize para
// devolver pretos profundos e brancos puros (característico de mangá/HQ).
var (
	levelsEnabled = true
	blackPoint    = float32(30)
	whitePoint    = float32(225)
)

// Parâmetros do escudo central (center-weighted energy): protege o centro da
// imagem (personagens/rostos) de serem "esmagados" pelo seam carving.
var (
	shieldEnabled  = true
	shieldStrength = 10.0
)

// maskProtected é o multiplicador de energia aplicado a pixels marcados como
// protegidos (branco) na máscara de proteção. Um valor enorme (~infinito)
// impede que esses pixels sejam cortados.
const maskProtected = 1e6

// UseForwardEnergy liga/desliga a energia forward (recomendada) vs backward.
func UseForwardEnergy(v bool) {
	forward = v
}

// SetLevels configura o filtro de níveis (contraste). blackPoint é o valor que
// vira preto puro, whitePoint o que vira branco puro. Desligue com enabled=false.
func SetLevels(black, white float32, enabled bool) {
	blackPoint = black
	whitePoint = white
	levelsEnabled = enabled
}

// SetCenterShield configura o escudo central: strength é o multiplicador máximo
// de energia aplicado no centro (nas bordas cai para 1x). Desligue com
// enabled=false.
func SetCenterShield(strength float64, enabled bool) {
	shieldStrength = strength
	shieldEnabled = enabled
}

// Resize redimensiona a imagem para width x height usando seam carving, primeiro
// na largura e depois na altura (ordem width-first). Os planos de cor e o mapa
// de cinza são mantidos em sincronia durante as operações de costura.
func Resize(src image.Image, width, height int) image.Image {
	return ResizeWithMask(src, nil, width, height)
}

// ResizeWithMask é como Resize, mas aceita uma máscara de proteção em tons de
// cinza (imagem B&W) na qual os pixels claros (branco) recebem energia
// "infinita" e nunca são cortados pelo seam carving; os escuros (preto) podem
// ser removidos normalmente. Útil para proteger personagens/objetos do centro.
// Passe nil para não usar máscara.
func ResizeWithMask(src, mask image.Image, width, height int) image.Image {
	h, w := src.Bounds().Dy(), src.Bounds().Dx()
	if h <= 0 || w <= 0 {
		return src
	}
	gray, r, g, b, m := toPlanes(src, mask, h, w)

	if width != w {
		gray, r, g, b, m, w = resizeWidth(gray, r, g, b, m, h, w, width)
	}
	if height != h {
		// Transpõe, redimensiona a largura (agora a altura original) e transpõe de volta.
		gray, r, g, b, m = transpose(gray, r, g, b, m, h, w)
		h, w = w, h
		gray, r, g, b, m, w = resizeWidth(gray, r, g, b, m, h, w, height)
		gray, r, g, b, m = transpose(gray, r, g, b, m, h, w)
		h, w = w, h
	}

	// Aplica a correção de contraste (levels) ANTES de remontar a imagem, para
	// devolver pretos profundos e brancos puros (o carving + compressão tende a
	// "lavar" a imagem).
	if levelsEnabled {
		applyLevels(r, g, b, blackPoint, whitePoint)
	}

	return fromPlanes(r, g, b, h, w)
}

// resizeWidth ajusta a largura para targetW, reduzindo ou expandindo conforme
// necessário. A expansão usa passos graduais (step_ratio = 0.5) para melhor
// qualidade, recomputando a energia a cada passo.
func resizeWidth(gray []float32, r, g, b []uint8, m []float32, h, w, targetW int) ([]float32, []uint8, []uint8, []uint8, []float32, int) {
	switch {
	case targetW < w:
		delta := w - targetW
		mask := getSeams(gray, m, h, w, delta, forward)
		gray, r, g, b, m, w = removeSeams(gray, r, g, b, m, h, w, mask)
	case targetW > w:
		delta := targetW - w
		for delta > 0 {
			step := int(math.Round(0.5 * float64(w)))
			if step < 1 {
				step = 1
			}
			if step > delta {
				step = delta
			}
			mask := getSeams(gray, m, h, w, step, forward)
			gray, r, g, b, m, w = insertSeams(gray, r, g, b, m, h, w, mask, step)
			gray = rgbToGray(r, g, b, h, w)
			delta -= step
		}
	}
	return gray, r, g, b, m, w
}

// getSeams encontra num costuras (uma por linha) e devolve uma máscara h*w onde
// cada pixel marcado pertence a uma costura (na posição original). Usa um mapa
// de índices para registrar onde cada costura cairia na imagem original.
func getSeams(gray, m []float32, h, w, num int, useForward bool) []bool {
	mask := make([]bool, h*w)
	idxMap := make([]int32, h*w)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			idxMap[y*w+x] = int32(x)
		}
	}

	cur := gray
	cm := m
	cw := w
	var energy []float32
	if !useForward {
		energy = getEnergy(cur, cm, h, cw)
	}

	for k := 0; k < num; k++ {
		var seam []int32
		if useForward {
			seam = getForwardSeam(cur, cm, h, cw)
		} else {
			seam = getBackwardSeam(energy, h, cw)
		}

		for y := 0; y < h; y++ {
			c := int(seam[y])
			mask[y*w+int(idxMap[y*cw+c])] = true
		}

		// Remove a costura do gray, da máscara e do mapa de índices (mantém as
		// três alinhadas conforme as colunas são removidas).
		ncw := cw - 1
		ncur := make([]float32, h*ncw)
		nm := make([]float32, h*ncw)
		nidx := make([]int32, h*ncw)
		for y := 0; y < h; y++ {
			c := int(seam[y])
			sy := y * cw
			ny := y * ncw
			copy(ncur[ny:ny+c], cur[sy:sy+c])
			copy(ncur[ny+c:ny+ncw], cur[sy+c+1:sy+cw])
			copy(nm[ny:ny+c], cm[sy:sy+c])
			copy(nm[ny+c:ny+ncw], cm[sy+c+1:sy+cw])
			copy(nidx[ny:ny+c], idxMap[sy:sy+c])
			copy(nidx[ny+c:ny+ncw], idxMap[sy+c+1:sy+cw])
		}
		cur = ncur
		cm = nm
		idxMap = nidx
		cw = ncw

		if !useForward {
			energy = getEnergy(cur, cm, h, cw)
		}
	}
	return mask
}

// removeSeams compacta cada linha removendo os pixels marcados na máscara.
// Cada linha deve ter exatamente o mesmo número de pixels marcados.
func removeSeams(gray []float32, r, g, b []uint8, m []float32, h, w int, mask []bool) ([]float32, []uint8, []uint8, []uint8, []float32, int) {
	removed := 0
	for x := 0; x < w; x++ {
		if mask[x] {
			removed++
		}
	}
	nw := w - removed

	ng := make([]float32, h*nw)
	nr := make([]uint8, h*nw)
	ngg := make([]uint8, h*nw)
	nb := make([]uint8, h*nw)
	nm := make([]float32, h*nw)
	for y := 0; y < h; y++ {
		sy := y * w
		ny := y * nw
		j := 0
		for x := 0; x < w; x++ {
			if mask[sy+x] {
				continue
			}
			ng[ny+j] = gray[sy+x]
			nr[ny+j] = r[sy+x]
			ngg[ny+j] = g[sy+x]
			nb[ny+j] = b[sy+x]
			nm[ny+j] = m[sy+x]
			j++
		}
	}
	return ng, nr, ngg, nb, nm, nw
}

// insertSeams insere delta pixels em cada linha ao longo das costuras marcadas,
// interpolando a média entre o pixel à esquerda e o atual.
func insertSeams(gray []float32, r, g, b []uint8, m []float32, h, w int, mask []bool, delta int) ([]float32, []uint8, []uint8, []uint8, []float32, int) {
	nw := w + delta
	nr := make([]uint8, h*nw)
	ngg := make([]uint8, h*nw)
	nb := make([]uint8, h*nw)
	nm := make([]float32, h*nw)

	for y := 0; y < h; y++ {
		sy := y * w
		ny := y * nw
		dst := 0
		for x := 0; x < w; x++ {
			if mask[sy+x] {
				left := x - 1
				if left < 0 {
					left = 0
				}
				nr[ny+dst] = uint8((int(r[sy+left]) + int(r[sy+x])) / 2)
				ngg[ny+dst] = uint8((int(g[sy+left]) + int(g[sy+x])) / 2)
				nb[ny+dst] = uint8((int(b[sy+left]) + int(b[sy+x])) / 2)
				nm[ny+dst] = m[sy+x]
				dst++
			}
			nr[ny+dst] = r[sy+x]
			ngg[ny+dst] = g[sy+x]
			nb[ny+dst] = b[sy+x]
			nm[ny+dst] = m[sy+x]
			dst++
		}
	}

	// Reconstrói o gray a partir dos planos de cor.
	ng := rgbToGray(nr, ngg, nb, h, nw)
	return ng, nr, ngg, nb, nm, nw
}

// getBackwardSeam encontra a costura de energia mínima via programação dinâmica
// sobre o mapa de energia (abordagem backward).
func getBackwardSeam(energy []float32, h, w int) []int32 {
	inf := float32(math.Inf(1))
	cost := make([]float32, w+2)
	cost[0] = inf
	cost[w+1] = inf
	for j := 0; j < w; j++ {
		cost[j+1] = energy[j]
	}
	parent := make([][]int32, h)

	for r := 1; r < h; r++ {
		parentRow := make([]int32, w)
		best := make([]float32, w)
		for j := 0; j < w; j++ {
			bv := cost[j]
			p := int32(j - 1)
			if cost[j+1] < bv {
				bv = cost[j+1]
				p = int32(j)
			}
			if cost[j+2] < bv {
				bv = cost[j+2]
				p = int32(j + 1)
			}
			best[j] = bv
			parentRow[j] = p
		}
		for j := 0; j < w; j++ {
			cost[j+1] = best[j] + energy[r*w+j]
		}
		parent[r] = parentRow
	}

	c := 0
	bv := cost[1]
	for j := 1; j < w; j++ {
		if cost[j+1] < bv {
			bv = cost[j+1]
			c = j
		}
	}

	seam := make([]int32, h)
	seam[h-1] = int32(c)
	for r := h - 2; r >= 0; r-- {
		seam[r] = parent[r+1][seam[r+1]]
	}
	return seam
}

// getForwardSeam encontra a costura usando energia forward, que penaliza a
// energia introduzida ao remover o pixel (melhor qualidade visual). Também
// respeita o escudo central e a máscara de proteção.
func getForwardSeam(gray, m []float32, h, w int) []int32 {
	pw := w + 2
	pad := make([]float32, h*pw)
	for r := 0; r < h; r++ {
		sy := r * w
		py := r * pw
		pad[py] = gray[sy]
		for c := 0; c < w; c++ {
			pad[py+1+c] = gray[sy+c]
		}
		pad[py+pw-1] = gray[sy+w-1]
	}

	inf := float32(math.Inf(1))
	dp := make([]float32, pw)
	for j := 0; j < w; j++ {
		dp[j+1] = float32(math.Abs(float64(pad[j+2]-pad[j])))*m[j] + maskPenalty(m[j])
	}
	dp[0] = inf
	dp[pw-1] = inf

	parent := make([][]int32, h)
	for r := 1; r < h; r++ {
		pr := r * pw
		pp := (r - 1) * pw
		parentRow := make([]int32, w)
		best := make([]float32, w)
		for j := 0; j < w; j++ {
			idx := pr + 1 + j
			currShl := pad[idx+1]
			currShr := pad[idx-1]
			prevMid := pad[pp+1+j]
			costMid := float32(math.Abs(float64(currShl - currShr)))
			costLeft := costMid + float32(math.Abs(float64(prevMid-currShr)))
			costRight := costMid + float32(math.Abs(float64(prevMid-currShl)))

			// Escudo central + máscara de proteção: multiplica e soma a penalidade
			// para que pixels protegidos (mesmo de energia zero) não sejam cortados.
			cw := centerWeight(w, h, j, r) * m[r*w+j]
			pen := maskPenalty(m[r*w+j])
			costMid = costMid*cw + pen
			costLeft = costLeft*cw + pen
			costRight = costRight*cw + pen

			vL := costLeft + dp[j]
			vM := costMid + dp[j+1]
			vR := costRight + dp[j+2]
			bv := vL
			p := int32(j - 1)
			if vM < bv {
				bv = vM
				p = int32(j)
			}
			if vR < bv {
				bv = vR
				p = int32(j + 1)
			}
			best[j] = bv
			parentRow[j] = p
		}
		for j := 0; j < w; j++ {
			dp[j+1] = best[j]
		}
		parent[r] = parentRow
	}

	c := 0
	bv := dp[1]
	for j := 1; j < w; j++ {
		if dp[j+1] < bv {
			bv = dp[j+1]
			c = j
		}
	}

	seam := make([]int32, h)
	seam[h-1] = int32(c)
	for r := h - 2; r >= 0; r-- {
		seam[r] = parent[r+1][seam[r+1]]
	}
	return seam
}

// maskPenalty devolve uma energia adicional (muito grande) para pixels
// protegidos. Soma-se (não apenas multiplica-se) porque multiplicar um pixel de
// energia zero por um fator alto continua 0, permitindo que ele fosse cortado.
func maskPenalty(m float32) float32 {
	if m > 1 {
		return maskProtected
	}
	return 0
}

// getEnergy computa o mapa de energia backward (gradiente Sobel, magnitude L1)
// com uma penalidade pesada de distanciamento para proteger o centro da arte e
// com o multiplicador de máscara (pixels protegidos têm energia enorme).
func getEnergy(gray, m []float32, h, w int) []float32 {
	energy := make([]float32, h*w)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			gx := sobelX(gray, h, w, x, y)
			gy := sobelY(gray, h, w, x, y)
			idx := y*w + x
			baseEnergy := float32(math.Abs(float64(gx)) + math.Abs(float64(gy)))
			energy[idx] = baseEnergy*centerWeight(w, h, x, y)*m[idx] + maskPenalty(m[idx])
		}
	}
	return energy
}

// centerWeight devolve um multiplicador de energia que cresce em direção ao
// centro da imagem (escudo protetor). No centro o peso é ~(1+strength)x maior;
// nas bordas extremas cai para 1x. Com strength=0 ou shield desligado, devolve
// sempre 1 (sem proteção).
func centerWeight(w, h, x, y int) float32 {
	if !shieldEnabled || shieldStrength <= 0 {
		return 1
	}
	centerX := float64(w) / 2.0
	centerY := float64(h) / 2.0
	maxDist := math.Sqrt(centerX*centerX + centerY*centerY)
	if maxDist <= 0 {
		return 1
	}
	dx := float64(x) - centerX
	dy := float64(y) - centerY
	dist := math.Sqrt(dx*dx + dy*dy)
	if dist > maxDist {
		dist = maxDist
	}
	return float32(1.0 + (1.0-dist/maxDist)*shieldStrength)
}

func sobelX(gray []float32, h, w, x, y int) float32 {
	var gx float32
	for dy := -1; dy <= 1; dy++ {
		yy := refl(y+dy, h)
		for dx := -1; dx <= 1; dx++ {
			xx := refl(x+dx, w)
			var k float32
			switch {
			case dx == 1:
				k = 1
				if dy == 0 {
					k = 2
				}
			case dx == -1:
				k = -1
				if dy == 0 {
					k = -2
				}
			}
			gx += k * gray[yy*w+xx]
		}
	}
	return gx
}

func sobelY(gray []float32, h, w, x, y int) float32 {
	var gy float32
	for dy := -1; dy <= 1; dy++ {
		yy := refl(y+dy, h)
		for dx := -1; dx <= 1; dx++ {
			xx := refl(x+dx, w)
			var k float32
			switch {
			case dy == 1:
				k = 1
				if dx == 0 {
					k = 2
				}
			case dy == -1:
				k = -1
				if dx == 0 {
					k = -2
				}
			}
			gy += k * gray[yy*w+xx]
		}
	}
	return gy
}

// refl aplica indexação espelhada (reflect) para bordas, como no scipy.
func refl(idx, n int) int {
	if idx < 0 {
		idx = -idx
	}
	if idx >= n {
		idx = 2*n - 2 - idx
	}
	return idx
}

// toPlanes converte uma image.Image em planos de cinza (float32), RGB (uint8) e
// máscara de proteção (float32, multiplicador de energia), com dimensões h x w.
// A máscara (imagem B&W opcional): pixels claros (branco) recebem maskProtected
// (não podem ser cortados); escuros recebem 1 (sem proteção). nil = sem máscara.
func toPlanes(img, mask image.Image, h, w int) ([]float32, []uint8, []uint8, []uint8, []float32) {
	gray := make([]float32, h*w)
	r := make([]uint8, h*w)
	g := make([]uint8, h*w)
	b := make([]uint8, h*w)
	m := make([]float32, h*w)
	for i := range m {
		m[i] = 1
	}

	if mask != nil {
		mb := mask.Bounds()
		moff := mb.Min
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				sy := y
				if sy < mb.Min.Y {
					sy = mb.Min.Y
				} else if sy >= mb.Max.Y {
					sy = mb.Max.Y - 1
				}
				sx := x
				if sx < mb.Min.X {
					sx = mb.Min.X
				} else if sx >= mb.Max.X {
					sx = mb.Max.X - 1
				}
				c := color.GrayModel.Convert(mask.At(moff.X+sx, moff.Y+sy)).(color.Gray)
				if c.Y > 127 {
					m[y*w+x] = maskProtected
				}
			}
		}
	}

	off := img.Bounds().Min
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := color.RGBAModel.Convert(img.At(off.X+x, off.Y+y)).(color.RGBA)
			idx := y*w + x
			r[idx] = c.R
			g[idx] = c.G
			b[idx] = c.B
			gray[idx] = 0.2126*float32(c.R) + 0.7152*float32(c.G) + 0.0722*float32(c.B)
		}
	}
	return gray, r, g, b, m
}

// rgbToGray reconstrói o mapa de cinza a partir dos planos RGB.
func rgbToGray(r, g, b []uint8, h, w int) []float32 {
	gray := make([]float32, h*w)
	for i := 0; i < h*w; i++ {
		gray[i] = 0.2126*float32(r[i]) + 0.7152*float32(g[i]) + 0.0722*float32(b[i])
	}
	return gray
}

// transpose transpoe os planos (troca linhas por colunas), mantendo a máscara.
func transpose(gray []float32, r, g, b []uint8, m []float32, h, w int) ([]float32, []uint8, []uint8, []uint8, []float32) {
	ng := make([]float32, h*w)
	nm := make([]float32, h*w)
	nr := make([]uint8, h*w)
	ngg := make([]uint8, h*w)
	nb := make([]uint8, h*w)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			src := y*w + x
			dst := x*h + y
			ng[dst] = gray[src]
			nm[dst] = m[src]
			nr[dst] = r[src]
			ngg[dst] = g[src]
			nb[dst] = b[src]
		}
	}
	return ng, nr, ngg, nb, nm
}

// applyLevels estica o histograma da imagem (simulando a ferramenta Levels do
// Photoshop) em cada canal RGB. Valores <= blackPoint viram 0 (preto puro);
// valores >= whitePoint viram 255 (branco puro); o restante é interpolado para
// cobrir toda a escala 0-255.
func applyLevels(r, g, b []uint8, blackPoint, whitePoint float32) {
	if whitePoint <= blackPoint {
		return
	}
	rangeV := whitePoint - blackPoint
	for i := 0; i < len(r); i++ {
		r[i] = clampLevel(float32(r[i]), blackPoint, rangeV)
		g[i] = clampLevel(float32(g[i]), blackPoint, rangeV)
		b[i] = clampLevel(float32(b[i]), blackPoint, rangeV)
	}
}

// clampLevel mapeia um valor do intervalo [min, max] para 0-255.
func clampLevel(v, min, rangeV float32) uint8 {
	if v <= min {
		return 0
	}
	if v >= min+rangeV {
		return 255
	}
	return uint8(((v - min) / rangeV) * 255.0)
}

// fromPlanes monta uma image.RGBA a partir dos planos RGB.
func fromPlanes(r, g, b []uint8, h, w int) image.Image {
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < h*w; i++ {
		dst.Pix[i*4] = r[i]
		dst.Pix[i*4+1] = g[i]
		dst.Pix[i*4+2] = b[i]
		dst.Pix[i*4+3] = 255
	}
	return dst
}
