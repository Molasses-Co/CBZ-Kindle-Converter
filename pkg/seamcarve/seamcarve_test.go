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

// TestResizeComMascaraParcialRoteiaForaDaProtecao verifica que uma região
// protegida encostando numa borda não é cortada quando existe caminho livre.
func TestResizeComMascaraParcialRoteiaForaDaProtecao(t *testing.T) {
	const size = 30
	img := image.NewGray(image.Rect(0, 0, size, size))
	mask := image.NewGray(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			img.SetGray(x, y, color.Gray{Y: 90})
			mask.SetGray(x, y, color.Gray{Y: 0})
		}
	}
	// Bloco protegido (caracter) no canto esquerdo: colunas 0-8, todas as linhas.
	for y := 0; y < size; y++ {
		for x := 0; x < 9; x++ {
			img.SetGray(x, y, color.Gray{Y: 200})
			mask.SetGray(x, y, color.Gray{Y: 255})
		}
	}

	out := ResizeWithMask(img, mask, size-10, size).(*image.RGBA)

	// Reduzimos 10px mas o bloco protegido deve permanecer intacto na saída.
	block := 0
	for y := 0; y < out.Bounds().Dy(); y++ {
		for x := 0; x < out.Bounds().Dx(); x++ {
			r := out.RGBAAt(x, y)
			if r.R > 180 {
				block++
			}
		}
	}
	if block < 9*size-1 {
		t.Fatalf("região protegida foi cortada: restaram %d de %d pixels", block, 9*size)
	}
}
