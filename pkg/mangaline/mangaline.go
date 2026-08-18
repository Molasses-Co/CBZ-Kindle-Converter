// Package mangaline extrai as linhas estruturais de páginas de mangá/HQ usando
// o modelo MangaLineExtraction (Li et al. 2017) exportado para ONNX
// (erika.onnx + pesos externos erika.onnx.data), via onnxruntime.
//
// O modelo tem shape FIXA 1x1x1024x1024: a entrada é a página em grayscale no
// range 0-255 (sem normalização) e a saída também é 0-255, onde valores BAIXOS
// indicam linha/traço estrutural e valores ALTOS indicam fundo. Ele separa o
// traço estrutural da trama/screentone.
//
// As linhas extraídas alimentam a máscara de proteção do retargeting, de modo
// que objetos/estruturas detectados pela rede (e não por um limiar de gradiente
// cru) sejam preservados pelo redimensionamento.
//
// O modelo é carregado do disco (não embutido), pois o par .onnx/.data soma
// ~330 MB, inviável para go:embed. Assume que o ambiente onnxruntime já foi
// inicializado (ex.: por esrgan.InitRuntime no ProcessCBZ).
package mangaline

import (
	"fmt"
	"image"
	"image/color"
	"os"
	"path/filepath"
	"runtime"
	"sync"

	ort "github.com/yalue/onnxruntime_go"
	"golang.org/x/image/draw"
)

const (
	inputSize = 1024
	modelOnnx = "erika.onnx"
	modelData = "erika.onnx.data"
)

// LineThreshold separa linha (valor abaixo) do fundo (acima) no mapa de saída.
// Valores baixos = traço estrutural. Ajustável por página/conteúdo.
var LineThreshold = float32(100)

// Extractor encapsula a sessão de inferência da extração de linhas.
type Extractor struct {
	mu      sync.Mutex
	session *ort.AdvancedSession
	input   *ort.Tensor[float32]
	output  *ort.Tensor[float32]
}

// New cria a sessão a partir do diretório que contém erika.onnx (+ .data).
// Exige que o ambiente onnxruntime já esteja inicializado (esrgan.InitRuntime).
func New(modelDir string) (*Extractor, error) {
	if !ort.IsInitialized() {
		return nil, fmt.Errorf("onnxruntime não inicializado; chame esrgan.InitRuntime antes de New")
	}
	onnxPath := filepath.Join(modelDir, modelOnnx)
	if _, err := os.Stat(onnxPath); err != nil {
		return nil, fmt.Errorf("modelo de linhas não encontrado em %s: %w", onnxPath, err)
	}

	shape := ort.NewShape(1, 1, inputSize, inputSize)
	input, err := ort.NewEmptyTensor[float32](shape)
	if err != nil {
		return nil, fmt.Errorf("falha ao criar tensor de entrada: %w", err)
	}
	output, err := ort.NewEmptyTensor[float32](shape)
	if err != nil {
		input.Destroy()
		return nil, fmt.Errorf("falha ao criar tensor de saída: %w", err)
	}

	opts, err := ort.NewSessionOptions()
	if err != nil {
		input.Destroy()
		output.Destroy()
		return nil, err
	}
	defer opts.Destroy()
	opts.SetGraphOptimizationLevel(ort.GraphOptimizationLevelEnableAll)
	opts.SetIntraOpNumThreads(runtime.NumCPU())
	opts.AppendExecutionProviderDirectML(0)

	session, err := ort.NewAdvancedSession(
		onnxPath,
		[]string{"input"},
		[]string{"output"},
		[]ort.Value{input},
		[]ort.Value{output},
		opts,
	)
	if err != nil {
		input.Destroy()
		output.Destroy()
		return nil, fmt.Errorf("falha ao carregar sessão de extração de linhas: %w", err)
	}

	return &Extractor{session: session, input: input, output: output}, nil
}

// Close libera a sessão e os tensores.
func (e *Extractor) Close() error {
	var firstErr error
	if e.session != nil {
		if err := e.session.Destroy(); err != nil && firstErr == nil {
			firstErr = err
		}
		e.session = nil
	}
	if e.input != nil {
		if err := e.input.Destroy(); err != nil && firstErr == nil {
			firstErr = err
		}
		e.input = nil
	}
	if e.output != nil {
		if err := e.output.Destroy(); err != nil && firstErr == nil {
			firstErr = err
		}
		e.output = nil
	}
	return firstErr
}

// Extract redimensiona a imagem para 1024x1024 (grayscale 0-255, sem
// normalização), roda a inferência e devolve o mapa de linhas (1024*1024
// float32, valores baixos = linha estrutural).
func (e *Extractor) Extract(img image.Image) ([]float32, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	work := image.NewRGBA(image.Rect(0, 0, inputSize, inputSize))
	draw.ApproxBiLinear.Scale(work, work.Bounds(), img, img.Bounds(), draw.Over, nil)

	inData := e.input.GetData()
	for y := 0; y < inputSize; y++ {
		for x := 0; x < inputSize; x++ {
			c := work.RGBAAt(x, y)
			inData[y*inputSize+x] = luma(c.R, c.G, c.B)
		}
	}

	if err := e.session.Run(); err != nil {
		return nil, fmt.Errorf("erro na inferência de extração de linhas: %w", err)
	}

	// Copia a saída: o buffer do tensor é reutilizado entre chamadas.
	src := e.output.GetData()
	out := make([]float32, len(src))
	copy(out, src)
	return out, nil
}

// ProtectionMask extrai as linhas e devolve uma máscara B&W (gray) no tamanho da
// imagem original em que pixels protegidos (branco) correspondem a componentes
// de linha estrutural com área >= minComponent (pixels). Componentes pequenos
// (traços finos soltos) são ignorados para não superproteger a página.
func (e *Extractor) ProtectionMask(img image.Image, minComponent int) (image.Image, error) {
	line, err := e.Extract(img)
	if err != nil {
		return nil, err
	}

	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 {
		return nil, fmt.Errorf("imagem vazia")
	}

	// Amostra o mapa 1024x1024 de volta para w x h (nearest: preserva traços finos).
	protected := make([]bool, w*h)
	for y := 0; y < h; y++ {
		sy := y * inputSize / h
		if sy >= inputSize {
			sy = inputSize - 1
		}
		for x := 0; x < w; x++ {
			sx := x * inputSize / w
			if sx >= inputSize {
				sx = inputSize - 1
			}
			protected[y*w+x] = line[sy*inputSize+sx] < LineThreshold
		}
	}

	// Filtra por tamanho de componente conectado.
	filtered := filterComponents(protected, w, h, minComponent)

	mask := image.NewGray(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if filtered[y*w+x] {
				mask.SetGray(x, y, color.Gray{Y: 255})
			}
		}
	}
	return mask, nil
}

// filterComponents mantém apenas componentes conexos (4-vizinhança) com área
// >= minArea.
func filterComponents(in []bool, w, h, minArea int) []bool {
	out := make([]bool, w*h)
	visited := make([]bool, w*h)
	for start := 0; start < w*h; start++ {
		if visited[start] || !in[start] {
			continue
		}
		stack := []int{start}
		visited[start] = true
		area := 0
		for len(stack) > 0 {
			i := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			area++
			for _, d := range [4]int{-1, 1, -w, w} {
				j := i + d
				if j < 0 || j >= w*h {
					continue
				}
				if (i%w == 0 && d == -1) || (i%w == w-1 && d == 1) {
					continue
				}
				if visited[j] || !in[j] {
					continue
				}
				visited[j] = true
				stack = append(stack, j)
			}
		}
		if area >= minArea {
			// Recolore: segundo passe para marcar o componente.
			stack = []int{start}
			visited2 := make(map[int]bool)
			visited2[start] = true
			out[start] = true
			for len(stack) > 0 {
				i := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				for _, d := range [4]int{-1, 1, -w, w} {
					j := i + d
					if j < 0 || j >= w*h {
						continue
					}
					if (i%w == 0 && d == -1) || (i%w == w-1 && d == 1) {
						continue
					}
					if !in[j] || visited2[j] {
						continue
					}
					visited2[j] = true
					out[j] = true
					stack = append(stack, j)
				}
			}
		}
	}
	return out
}

// FindModelDir localiza o diretório que contém o modelo de linhas, na ordem:
// variável de ambiente MANGA_LINE_DIR, diretório "pkg/mangaline" relativo ao
// diretório de trabalho, diretório do executável e diretório de trabalho atual.
func FindModelDir() (string, error) {
	candidates := []string{}
	if p := os.Getenv("MANGA_LINE_DIR"); p != "" {
		candidates = append(candidates, p)
	}
	candidates = append(candidates, filepath.Join("pkg", "mangaline"))
	candidates = append(candidates, ".")
	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), "pkg", "mangaline"))
	}
	for _, dir := range candidates {
		if _, err := os.Stat(filepath.Join(dir, modelOnnx)); err == nil {
			return dir, nil
		}
	}
	return "", fmt.Errorf("modelo de linhas (%s) não encontrado; defina MANGA_LINE_DIR", modelOnnx)
}

func luma(r, g, b uint8) float32 {
	return .2126*float32(r) + .7152*float32(g) + .0722*float32(b)
}
