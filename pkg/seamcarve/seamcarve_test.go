package seamcarve

import (
	"image"
	"image/color"
	"testing"
)

// TestResizeWithMaskProtegeStripe verifica que uma faixa branca marcada como
// protegida (branca) na máscara sobrevive ao seam carving, enquanto uma imagem
// sem máscara deixaria que a faixa fosse cortada.
func TestResizeWithMaskProtegeStripe(t *testing.T) {
	const size = 20
	img := image.NewGray(image.Rect(0, 0, size, size))
	mask := image.NewGray(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			img.SetGray(x, y, color.Gray{Y: 100})
			mask.SetGray(x, y, color.Gray{Y: 0})
		}
	}
	// Faixa vertical branca protegida na coluna x=10.
	for y := 0; y < size; y++ {
		img.SetGray(10, y, color.Gray{Y: 255})
		mask.SetGray(10, y, color.Gray{Y: 255})
	}

	out := ResizeWithMask(img, mask, size/2, size).(*image.RGBA)

	white := 0
	for y := 0; y < out.Bounds().Dy(); y++ {
		for x := 0; x < out.Bounds().Dx(); x++ {
			r := out.RGBAAt(x, y)
			if r.R > 200 && r.G > 200 && r.B > 200 {
				white++
			}
		}
	}
	if white < size-2 {
		t.Fatalf("máscara não protegeu: apenas %d de %d pixels brancos sobreviveram", white, size)
	}
}

// TestResizeSemMaskPodeCortar a mesma imagem SEM máscara costuma remover a faixa
// (comportamento anterior). Confirma que a proteção é o que muda o resultado.
func TestResizeSemMaskPodeCortar(t *testing.T) {
	const size = 20
	img := image.NewGray(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			img.SetGray(x, y, color.Gray{Y: 100})
		}
	}
	for y := 0; y < size; y++ {
		img.SetGray(10, y, color.Gray{Y: 255})
	}

	out := Resize(img, size/2, size).(*image.RGBA)

	white := 0
	for y := 0; y < out.Bounds().Dy(); y++ {
		for x := 0; x < out.Bounds().Dx(); x++ {
			r := out.RGBAAt(x, y)
			if r.R > 200 && r.G > 200 && r.B > 200 {
				white++
			}
		}
	}
	if white >= size {
		t.Fatalf("esperava remoção da faixa sem máscara, mas %d pixels brancos permaneceram", white)
	}
}
