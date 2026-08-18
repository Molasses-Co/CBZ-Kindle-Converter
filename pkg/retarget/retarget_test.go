package retarget

import (
	"image"
	"image/color"
	"math"
	"testing"
)

// buildProbe monta uma imagem de fundo branco com um bloco saliente (linha).
func buildProbe(w, h, bx0, by0, bw, bh int) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: 240, G: 240, B: 240, A: 255})
		}
	}
	for y := by0; y < by0+bh; y++ {
		for x := bx0; x < bx0+bw; x++ {
			img.Set(x, y, color.RGBA{R: 10, G: 10, B: 10, A: 255})
		}
	}
	return img
}

// blockBounds localiza o retângulo de pixels não-brancos (o bloco saliente).
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

// TestFitCabeSemCrop garante que, ao mudar o aspect ratio, o bloco permanece
// intacto (nada é cortado) e o aspect ratio da página é preservado.
func TestFitCabeSemCrop(t *testing.T) {
	// Origem 600x900 (aspecto 2:3); alvo 800x700 (aspecto diferente).
	img := buildProbe(600, 900, 240, 330, 120, 240)
	out := Fit(img, 800, 700)

	// Escala = min(800/600, 700/900) = min(1.333, 0.778) = 0.778 → 467x700.
	if out.Bounds().Dx() != 467 || out.Bounds().Dy() != 700 {
		t.Fatalf("dimensão do resultado errada: %dx%d", out.Bounds().Dx(), out.Bounds().Dy())
	}

	// O bloco inteiro continua presente (nada foi cortado).
	x0, y0, x1, y1 := blockBounds(out)
	if x0 < 0 || y0 < 0 || x1 >= out.Bounds().Dx() || y1 >= out.Bounds().Dy() {
		t.Fatalf("bloco cortado: (%d,%d)-(%d,%d)", x0, y0, x1, y1)
	}

	// Aspecto do bloco preservado (escala uniforme, sem shear).
	gotAspect := float64(x1-x0+1) / float64(y1-y0+1)
	wantAspect := 120.0 / 240.0
	t.Logf("bloco resultante: %dx%d (aspecto %.3f, esperado %.3f)", x1-x0+1, y1-y0+1, gotAspect, wantAspect)
	if math.Abs(gotAspect-wantAspect) > 0.05 {
		t.Errorf("aspecto do bloco não preservado: got=%.3f want=%.3f", gotAspect, wantAspect)
	}
}

// TestFitMesmoAspect: quando o aspect coincide com o alvo, o resultado tem
// exatamente dstW x dstH (sem barras).
func TestFitMesmoAspect(t *testing.T) {
	img := buildProbe(600, 900, 240, 330, 120, 240)
	out := Fit(img, 400, 600) // mesmo aspect ratio 2:3
	if out.Bounds().Dx() != 400 || out.Bounds().Dy() != 600 {
		t.Fatalf("dimensão errada: %dx%d", out.Bounds().Dx(), out.Bounds().Dy())
	}
}

// TestFitNaoUpscale: se a origem já cabe no alvo, não amplia (apenas ajusta para
// o maior tamanho possível sem ultrapassar o alvo).
func TestFitNaoExcedeAlvo(t *testing.T) {
	img := buildProbe(600, 900, 240, 330, 120, 240)
	out := Fit(img, 1000, 1200) // alvo maior que a origem
	if out.Bounds().Dx() > 1000 || out.Bounds().Dy() > 1200 {
		t.Fatalf("excedeu o alvo: %dx%d", out.Bounds().Dx(), out.Bounds().Dy())
	}
	// Não ultrapassa o alvo e preserva o aspect 2:3.
	if out.Bounds().Dx() != 800 || out.Bounds().Dy() != 1200 {
		t.Fatalf("escala errada: %dx%d (esperado 800x1200)", out.Bounds().Dx(), out.Bounds().Dy())
	}
}
