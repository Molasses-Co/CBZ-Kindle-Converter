package services

import (
	"image"
	"image/color"
	"os"
	"path/filepath"
	"testing"

	_ "golang.org/x/image/webp"
)

func luma(c color.RGBA) float64 { return 0.2126*float64(c.R) + 0.7152*float64(c.G) + 0.0722*float64(c.B) }
func mean(img image.Image) float64 {
	b := img.Bounds()
	var s, n float64
	for y := b.Min.Y; y < b.Max.Y; y += 3 {
		for x := b.Min.X; x < b.Max.X; x += 3 {
			c := color.RGBAModel.Convert(img.At(x, y)).(color.RGBA)
			s += luma(c)
			n++
		}
	}
	return s / n
}

// TestLightenAumentaBrilho garante que a correção gama clareia a imagem (lift
// positivo) sem alterar as dimensões.
func TestLightenAumentaBrilho(t *testing.T) {
	f, err := os.Open(filepath.Join("..", "..", "test-img", "013.webp"))
	if err != nil {
		t.Skipf("skip: %v", err)
	}
	defer f.Close()
	src, _, err := image.Decode(f)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	before := mean(src)
	out := lighten(src, brightnessGamma)
	b := out.Bounds()
	if b.Dx() != src.Bounds().Dx() || b.Dy() != src.Bounds().Dy() {
		t.Fatalf("lighten mudou dimensoes: %dx%d -> %dx%d", src.Bounds().Dx(), src.Bounds().Dy(), b.Dx(), b.Dy())
	}
	after := mean(out)
	if after <= before {
		t.Fatalf("lighten(g=%.2f) nao clareou: luma %.2f -> %.2f", brightnessGamma, before, after)
	}
	if after-before > 8 {
		t.Fatalf("clareamento excessivo: luma %.2f -> %.2f (delta %.2f)", before, after, after-before)
	}
}
