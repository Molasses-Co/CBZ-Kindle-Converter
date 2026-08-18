package retarget

import (
	"image"
	"image/color"
	"math"
	"testing"
)

// buildProbe monta uma imagem de fundo branco com um bloco saliente (linha)
// e devolve também a máscara protegendo apenas esse bloco.
func buildProbe(w, h, bx0, by0, bw, bh int) (image.Image, image.Image) {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	mask := image.NewGray(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: 240, G: 240, B: 240, A: 255})
		}
	}
	for y := by0; y < by0+bh; y++ {
		for x := bx0; x < bx0+bw; x++ {
			img.Set(x, y, color.RGBA{R: 10, G: 10, B: 10, A: 255})
			mask.SetGray(x, y, color.Gray{Y: 255})
		}
	}
	return img, mask
}

// blockBounds localiza o retângulo de pixels não-brancos (o bloco protegido).
func blockBounds(img image.Image) (x0, y0, x1, y1 int) {
	b := img.Bounds()
	x0, y0, x1, y1 = 1<<30, 1<<30, -1, -1
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			c := color.NRGBAModel.Convert(img.At(x, y)).(color.NRGBA)
			if c.R < 200 {
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
		}
	}
	return
}

// TestCoverCropPreservaAspecto garante que, ao mudar o aspect ratio, o bloco
// saliente mantém o aspecto (escala uniforme, sem shear/stretch) e permanece
// dentro da janela de crop (não é cortado).
func TestCoverCropPreservaAspecto(t *testing.T) {
	// Origem 600x900; destino 800x700 (aspecto muda). Bloco 120x240 centrado.
	img, mask := buildProbe(600, 900, 240, 330, 120, 240)
	out := Fit(img, mask, 800, 700)

	if out.Bounds().Dx() != 800 || out.Bounds().Dy() != 700 {
		t.Fatalf("dimensão do resultado errada: %dx%d", out.Bounds().Dx(), out.Bounds().Dy())
	}

	x0, y0, x1, y1 := blockBounds(out)
	if x0 < 0 || y0 < 0 || x1 >= 800 || y1 >= 700 {
		t.Fatalf("bloco cortado pelo crop: (%d,%d)-(%d,%d)", x0, y0, x1, y1)
	}
	gotW, gotH := float64(x1-x0+1), float64(y1-y0+1)
	gotAspect := gotW / gotH
	wantAspect := 120.0 / 240.0
	t.Logf("bloco resultante: %.0fx%.0f (aspecto %.3f, esperado %.3f)", gotW, gotH, gotAspect, wantAspect)
	if math.Abs(gotAspect-wantAspect) > 0.05 {
		t.Errorf("aspecto do bloco não preservado: got=%.3f want=%.3f", gotAspect, wantAspect)
	}
}

// TestFitMesmoAspect usa interpolação simples (sem retargeting) quando o
// aspect ratio não muda.
func TestFitMesmoAspect(t *testing.T) {
	img, _ := buildProbe(600, 900, 240, 330, 120, 240)
	out := Fit(img, nil, 400, 600) // mesmo aspect ratio 2:3
	if out.Bounds().Dx() != 400 || out.Bounds().Dy() != 600 {
		t.Fatalf("dimensão errada: %dx%d", out.Bounds().Dx(), out.Bounds().Dy())
	}
}

// TestFitSemMascara apenas redimensiona.
func TestFitSemMascara(t *testing.T) {
	img, _ := buildProbe(600, 900, 240, 330, 120, 240)
	out := Fit(img, nil, 800, 700)
	if out.Bounds().Dx() != 800 || out.Bounds().Dy() != 700 {
		t.Fatalf("dimensão errada: %dx%d", out.Bounds().Dx(), out.Bounds().Dy())
	}
}

// TestSameAspect verifica a tolerância do comparador.
func TestSameAspect(t *testing.T) {
	if !SameAspect(600, 900, 400, 600) {
		t.Error("600x900 e 400x600 têm o mesmo aspect ratio")
	}
	if SameAspect(600, 900, 800, 700) {
		t.Error("600x900 e 800x700 têm aspects diferentes")
	}
}
