// Package retarget redimensiona páginas de mangá/HQ para uma resolução alvo.
//
// O alvo (dstW x dstH) é tratado como um LIMITE MÁXIMO: a página é escalada
// uniformemente para caber dentro dele, preservando o aspect ratio original.
// Nunca há crop, shear, estiramento ou barras de preenchimento — o resultado
// tem exatamente o tamanho da página redimensionada, sem perda de conteúdo.
package retarget

import (
	"image"
	"math"

	"golang.org/x/image/draw"
)

// Fit redimensiona src para caber dentro de dstW x dstH, preservando o aspect
// ratio. O tamanho final é o maior que cabe no alvo (ex.: 742x1200 com alvo
// 600x800 vira 495x800). Quando o aspect coincide com o alvo, o resultado tem
// exatamente dstW x dstH.
func Fit(src image.Image, dstW, dstH int) *image.RGBA {
	if dstW <= 0 || dstH <= 0 {
		return toRGBA(src)
	}
	b := src.Bounds()
	srcW, srcH := b.Dx(), b.Dy()
	if srcW == dstW && srcH == dstH {
		return toRGBA(src)
	}

	// Escala uniforme máxima que cabe dentro do alvo (min dos dois eixos).
	scale := math.Min(float64(dstW)/float64(srcW), float64(dstH)/float64(srcH))
	if scale <= 0 {
		return toRGBA(src)
	}
	w := int(math.Round(float64(srcW) * scale))
	h := int(math.Round(float64(srcH) * scale))
	if w < 1 {
		w = 1
	}
	if h < 1 {
		h = 1
	}
	return resize(src, w, h)
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
