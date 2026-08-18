package mangaline

import (
	"image"
	"image/color"
	"os"
	"testing"

	"cbz-converter/pkg/esrgan"
)

// TestProtectionMaskIntegration valida a extração de linhas contra o modelo
// real. Pula se a DLL do onnxruntime não estiver apontada por
// ONNXRUNTIME_TEST_DLL (a DLL DirectML não cabe no repositório).
func TestProtectionMaskIntegration(t *testing.T) {
	dll := os.Getenv("ONNXRUNTIME_TEST_DLL")
	if dll == "" {
		t.Skip("ONNXRUNTIME_TEST_DLL não configurado")
	}
	dir, err := FindModelDir()
	if err != nil {
		t.Skipf("modelo de linhas não encontrado: %v", err)
	}
	if err := esrgan.InitRuntime(dll); err != nil {
		t.Fatalf("InitRuntime: %v", err)
	}
	defer esrgan.DestroyRuntime()

	e, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer e.Close()

	im := image.NewRGBA(image.Rect(0, 0, 800, 1200))
	for y := 0; y < 1200; y++ {
		for x := 0; x < 800; x++ {
			im.Set(x, y, color.RGBA{R: 250, G: 250, B: 250, A: 255})
		}
	}
	// Traço diagonal estrutural espesso (cabo de foice).
	for i := 0; i < 700; i++ {
		for dy := -3; dy <= 3; dy++ {
			x, y := 100+i, 200+i+dy
			if x >= 0 && x < 800 && y >= 0 && y < 1200 {
				im.Set(x, y, color.RGBA{A: 255})
			}
		}
	}
	// Traço fino isolado (deve ser filtrado pelo minComponent).
	for x := 600; x < 610; x++ {
		im.Set(x, 60, color.RGBA{A: 255})
	}
	// Trama (screentone) em um canto (não deve ser tratada como linha estrutural).
	for y := 900; y < 1100; y++ {
		for x := 600; x < 780; x++ {
			if (x+y)%40 < 20 {
				im.Set(x, y, color.RGBA{R: 245, G: 245, B: 245, A: 255})
			}
		}
	}

	mask, err := e.ProtectionMask(im, 400)
	if err != nil {
		t.Fatalf("ProtectionMask: %v", err)
	}
	b := mask.Bounds()
	var prot, tiny, screen int
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			c := color.GrayModel.Convert(mask.At(x, y)).(color.Gray)
			if c.Y == 0 {
				continue
			}
			if x >= 600 && x < 610 && y >= 60 && y < 70 {
				tiny++
			} else if x >= 600 && y >= 900 && x < 780 && y < 1100 {
				screen++
			} else {
				prot++
			}
		}
	}
	t.Logf("protected=%d tiny=%d screentone=%d", prot, tiny, screen)
	if prot < 1000 {
		t.Errorf("esperava proteger o traço diagonal estrutural, protegidos=%d", prot)
	}
	if tiny > 20 {
		t.Errorf("traço fino isolado deveria ser filtrado, tiny=%d", tiny)
	}
	if screen > 100 {
		t.Errorf("screentone não deveria ser protegido como linha estrutural, screen=%d", screen)
	}
}
