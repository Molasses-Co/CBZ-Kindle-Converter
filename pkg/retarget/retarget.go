// Package retarget redimensiona páginas de mangá/HQ para uma resolução alvo.
//
// Princípio (pesquisa de mangá): nunca introduzir shear ou estiramento que
// deforme a arte. Quando o aspect ratio NÃO muda, basta interpolação uniforme
// de alta qualidade. Quando o aspect ratio MUDA, aplica-se escala uniforme para
// COBRIR o alvo e recorta-se o excedente (crop), com a janela de corte
// posicionada para manter o conteúdo saliente (máscara de proteção). Isso
// preserva cores e proporções exatamente, sem shear, sem esticar e sem bordas
// brancas — ao custo de aparar as bordas/bleed.
package retarget

import (
	"image"
	"image/color"
	"math"

	"golang.org/x/image/draw"
)

// SameAspect reporta se dois tamanhos têm o mesmo aspect ratio (tolerância de
// 1%). Evita o crop quando apenas a resolução muda.
func SameAspect(w1, h1, w2, h2 int) bool {
	if h1 == 0 || h2 == 0 {
		return true
	}
	a1 := float64(w1) / float64(h1)
	a2 := float64(w2) / float64(h2)
	return math.Abs(a1-a2)/a1 < 0.01
}

// Fit redimensiona src para dstW x dstH. mask (gray B&W, branco = protegido)
// marca o conteúdo saliente a preservar do crop; pode ser nil (janela centrada).
// Se o aspect ratio coincidir, aplica interpolação uniforme. Caso contrário,
// escala uniforme para cobrir o alvo + crop centrado na saliência.
func Fit(src image.Image, mask image.Image, dstW, dstH int) image.Image {
	if dstW <= 0 || dstH <= 0 {
		return src
	}
	b := src.Bounds()
	srcW, srcH := b.Dx(), b.Dy()
	if srcW == dstW && srcH == dstH {
		return src
	}

	// Mesmo aspect ratio: apenas interpolação uniforme (sem crop).
	if SameAspect(srcW, srcH, dstW, dstH) {
		return resize(src, dstW, dstH)
	}

	return coverCrop(src, mask, srcW, srcH, dstW, dstH)
}

// coverCrop escala src uniformemente para cobrir dstW x dstH e recorta o
// excedente com a janela centrada no conteúdo saliente da máscara.
func coverCrop(src image.Image, mask image.Image, srcW, srcH, dstW, dstH int) image.Image {
	scale := math.Max(float64(dstW)/float64(srcW), float64(dstH)/float64(srcH))
	sw := int(math.Ceil(float64(srcW) * scale))
	sh := int(math.Ceil(float64(srcH) * scale))
	if sw < dstW {
		sw = dstW
	}
	if sh < dstH {
		sh = dstH
	}

	scaled := resize(src, sw, sh)

	cx, cy := cropWindow(mask, srcW, srcH, sw, sh, dstW, dstH)
	return crop(scaled, cx, cy, dstW, dstH)
}

// cropWindow devolve o canto superior esquerdo da janela de crop (dstW x dstH)
// dentro de sw x sh, centrada no bounding box do conteúdo protegido da máscara.
// Sem máscara (ou sem conteúdo), centra a janela.
func cropWindow(mask image.Image, srcW, srcH, sw, sh, dstW, dstH int) (int, int) {
	centerX := float64(sw) / 2
	centerY := float64(sh) / 2

	if mask != nil {
		mb := mask.Bounds()
		minX, minY, maxX, maxY := -1, -1, -1, -1
		gm := color.GrayModel
		for y := 0; y < srcH; y++ {
			for x := 0; x < srcW; x++ {
				if gm.Convert(mask.At(mb.Min.X+x, mb.Min.Y+y)).(color.Gray).Y > 0 {
					if minX == -1 || x < minX {
						minX = x
					}
					if x > maxX {
						maxX = x
					}
					if minY == -1 || y < minY {
						minY = y
					}
					if y > maxY {
						maxY = y
					}
				}
			}
		}
		if minX != -1 {
			// Centro do bbox protegido, mapeado para a escala do crop.
			centerX = (float64(minX+maxX)/2 + 0.5) * float64(sw) / float64(srcW)
			centerY = (float64(minY+maxY)/2 + 0.5) * float64(sh) / float64(srcH)
		}
	}

	cx := int(centerX) - dstW/2
	cy := int(centerY) - dstH/2
	if cx < 0 {
		cx = 0
	}
	if cy < 0 {
		cy = 0
	}
	if cx > sw-dstW {
		cx = sw - dstW
	}
	if cy > sh-dstH {
		cy = sh - dstH
	}
	return cx, cy
}

// crop extrai a janela (x, y, w, h) de img copiando para uma nova imagem.
func crop(img image.Image, x, y, w, h int) *image.RGBA {
	src := toRGBA(img)
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(dst, dst.Bounds(), src, image.Point{X: x, Y: y}, draw.Src)
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

// resize redimensiona com interpolação de alta qualidade (Catmull-Rom).
func resize(src image.Image, w, h int) *image.RGBA {
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, src.Bounds(), draw.Over, nil)
	return dst
}
