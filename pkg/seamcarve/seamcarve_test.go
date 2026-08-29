// Package seamcarve tests cover-fit behavior against the real test image.
package seamcarve

import (
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"testing"

	_ "golang.org/x/image/webp"
)

// genSolid gera uma imagem sólida de w x h (fundo cinza com uma faixa clara no
// centro) para validar o ajuste uniforme de aspect sem carving.
func genSolid(w, h int) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := color.RGBA{200, 200, 200, 255}
			if x > w/4 && x < 3*w/4 && y > h/4 && y < 3*h/4 {
				c = color.RGBA{50, 50, 50, 255}
			}
			img.SetRGBA(x, y, c)
		}
	}
	return img
}

// TestCoverImage013 ensure the real 013.webp is resized to EXACTLY the target
// box (no left-over margins) and that same-aspect inputs stay pure-interpolated.
func TestCoverImage013(t *testing.T) {
	imgFile := filepath.Join("..", "..", "test-img", "013.webp")
	f, err := os.Open(imgFile)
	if err != nil {
		t.Skipf("skip: %v", err)
	}
	defer f.Close()

	src, _, err := image.Decode(f)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	dstW, dstH := 1080, 1920
	out := Cover(src, dstW, dstH)
	b := out.Bounds()
	if b.Dx() != dstW || b.Dy() != dstH {
		t.Fatalf("Cover = %dx%d, want %dx%d (precisa preencher o alvo exato)", b.Dx(), b.Dy(), dstW, dstH)
	}

	// Salva para inspeção visual (vai para o temp do Go).
	outFile := filepath.Join(os.TempDir(), "manga_cover_013.jpg")
	of, err := os.Create(outFile)
	if err != nil {
		t.Fatalf("create out: %v", err)
	}
	defer of.Close()
	if err := jpeg.Encode(of, out, &jpeg.Options{Quality: 92}); err != nil {
		t.Fatalf("encode: %v", err)
	}
	t.Logf("salvo em %s", outFile)
}

// TestCoverSameAspect garante que uma imagem com o mesmo aspect do alvo não tem
// carving: o resultado é idêntico a uma interpolação uniforme para dstW x dstH.
func TestCoverSameAspect(t *testing.T) {
	src := genSolid(742, 1200)
	dstW, dstH := 742*2, 1200*2 // mesmo aspect, apenas maior
	out := Cover(src, dstW, dstH)
	b := out.Bounds()
	if b.Dx() != dstW || b.Dy() != dstH {
		t.Fatalf("Cover(same aspect) = %dx%d, want %dx%d", b.Dx(), b.Dy(), dstW, dstH)
	}
}
