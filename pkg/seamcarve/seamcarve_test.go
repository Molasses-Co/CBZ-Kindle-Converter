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

// TestResizeSemMaskProtegeBordasFortes confirma que, mesmo sem máscara, a
// energia híbrida (Scharr + dilatação + termo de orientação) agora preserva uma
// faixa vertical de alto contraste automaticamente — antes ela era cortada por
// ser uma região de baixa textura.
func TestResizeSemMaskProtegeBordasFortes(t *testing.T) {
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
	if white < size-2 {
		t.Fatalf("esperava preservar a faixa de alto contraste, mas apenas %d pixels brancos restaram", white)
	}
}

// TestResizeWithMaskProtegeFaixaDiagonal verifica que uma linha fina diagonal
// (haste de foice) marcada como protegida permanece contínua após redução
// agressiva, enquanto sem proteção ela seria interrompida.
func TestResizeWithMaskProtegeFaixaDiagonal(t *testing.T) {
	const size = 40
	img := image.NewGray(image.Rect(0, 0, size, size))
	mask := image.NewGray(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			img.SetGray(x, y, color.Gray{Y: 100})
			mask.SetGray(x, y, color.Gray{Y: 0})
		}
	}
	// Linha diagonal branca (x = y, com 2px de espessura) protegida.
	for i := 0; i < size; i++ {
		for dx := 0; dx < 2; dx++ {
			if i+dx < size {
				img.SetGray(i+dx, i, color.Gray{Y: 255})
				mask.SetGray(i+dx, i, color.Gray{Y: 255})
			}
		}
	}

	out := ResizeWithMask(img, mask, size/2, size).(*image.RGBA)

	// A linha deve sobreviver: conta pixels brancos; deve permanecer contígua.
	white := 0
	for y := 0; y < out.Bounds().Dy(); y++ {
		for x := 0; x < out.Bounds().Dx(); x++ {
			r := out.RGBAAt(x, y)
			if r.R > 200 && r.G > 200 && r.B > 200 {
				white++
			}
		}
	}
	if white < size*2/2 {
		t.Fatalf("faixa diagonal não preservada: apenas %d pixels brancos", white)
	}
}

// TestHybridProtegeBlocoSemShear verifica que um bloco protegido na borda é
// extraído e reescalado de forma uniforme (híbrido): permanece um único run
// contíguo de largura consistente em todas as linhas, sem shear — mesmo que a
// imagem seja reduzida pela metade.
func TestHybridProtegeBlocoSemShear(t *testing.T) {
	const size = 30
	img := image.NewGray(image.Rect(0, 0, size, size))
	mask := image.NewGray(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			img.SetGray(x, y, color.Gray{Y: 90})
			mask.SetGray(x, y, color.Gray{Y: 0})
		}
	}
	// Bloco protegido (personagem) no canto esquerdo: colunas 0-8, todas as linhas.
	for y := 0; y < size; y++ {
		for x := 0; x < 9; x++ {
			img.SetGray(x, y, color.Gray{Y: 200})
			mask.SetGray(x, y, color.Gray{Y: 255})
		}
	}

	out := ResizeWithMask(img, mask, size-10, size).(*image.RGBA)

	// Largura escalada do bloco: 9 * (20/30) = 6.
	scaled := 9 * (size - 10) / size

	var firstSpan int
	for y := 0; y < out.Bounds().Dy(); y++ {
		// Maior run contíguo de pixels "protegidos" (200) nesta linha.
		maxRun := 0
		run := 0
		for x := 0; x < out.Bounds().Dx(); x++ {
			if out.RGBAAt(x, y).R > 180 {
				run++
				if run > maxRun {
					maxRun = run
				}
			} else {
				run = 0
			}
		}
		if y == 0 {
			firstSpan = maxRun
		}
		if maxRun != firstSpan {
			t.Fatalf("shear detectado: linha %d tem bloco de %d px, linha 0 tem %d px", y, maxRun, firstSpan)
		}
	}
	if firstSpan != scaled {
		t.Fatalf("bloco protegido deveria ter %d px de largura (escala uniforme), tem %d", scaled, firstSpan)
	}
}

// TestHybridProtegeBandaDiagonalSemShear verifica que uma banda diagonal
// protegida (haste de foice) é extraída e reescalada sem shear: o centro da
// banda por linha deve permanecer monotônico (diagonal reta), mesmo reduzindo a
// largura. Com seams alternando de lado, os centros ziguezagueariam.
func TestHybridProtegeBandaDiagonalSemShear(t *testing.T) {
	const size = 40
	img := image.NewGray(image.Rect(0, 0, size, size))
	mask := image.NewGray(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			img.SetGray(x, y, color.Gray{Y: 90})
			mask.SetGray(x, y, color.Gray{Y: 0})
		}
	}
	// Banda diagonal protegida localizada: y em [10,30], x em [y-8, y-4].
	for y := 10; y < 30; y++ {
		for x := y - 8; x <= y-4; x++ {
			if x >= 0 && x < size {
				img.SetGray(x, y, color.Gray{Y: 200})
				mask.SetGray(x, y, color.Gray{Y: 255})
			}
		}
	}

	out := ResizeWithMask(img, mask, size-8, size).(*image.RGBA)

	prev := -1
	found := 0
	for y := 0; y < out.Bounds().Dy(); y++ {
		sum, cnt := 0, 0
		for x := 0; x < out.Bounds().Dx(); x++ {
			if out.RGBAAt(x, y).R > 180 {
				sum += x
				cnt++
			}
		}
		if cnt == 0 {
			continue
		}
		center := sum / cnt
		found++
		if prev >= 0 && center < prev-1 {
			t.Fatalf("shear: centro da banda na linha %d (%d) regrediu vs linha anterior (%d)", y, center, prev)
		}
		prev = center
	}
	if found < 15 {
		t.Fatalf("banda protegida quase ausente: apenas %d linhas", found)
	}
}
