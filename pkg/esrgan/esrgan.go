// Package esrgan encapsula a inferência do modelo Real-ESRGAN general-x4-v3
// (SRVGGNetCompact) x4 quantizado w8a8 via onnxruntime, usado para
// super-resolução de páginas que precisam de upscaling. A versão quantizada é
// mais leve e rápida que a float, sem perda visual perceptível em HQs/mangás.
//
// O execution provider DirectML é usado para acelerar a inferência na GPU
// quando disponível (fallback automático para CPU). As páginas são processadas
// em tiles (área efetiva menor com padding) distribuídos entre várias sessões
// executadas em paralelo.
package esrgan

import (
	"fmt"
	"image"
	"image/color"
	"os"
	"path/filepath"
	"runtime"
	"sync"

	"cbz-converter/pkg/ortdll"
	modeldata "cbz-converter/pkg/real_esrgan_general_x4v3-onnx-w8a8"

	ort "github.com/yalue/onnxruntime_go"
)

const (
	inputSize  = 128 // dimensão da entrada do modelo (1,3,128,128)
	outputSize = 512 // dimensão da saída (1,3,512,512)
	scale      = 4   // fator de upscale do modelo (512/128)
	modelOnnx  = "real_esrgan_general_x4v3.onnx"
	modelData  = "real_esrgan_general_x4v3.data"

	// A entrada do modelo é fixa em 128x128, mas para reduzir artefatos nas
	// bordas usamos um tile efetivo menor (64) centralizado no tensor de entrada,
	// com o restante preenchido por replicação (padding de 32). A região válida
	// da saída é a área central correspondente (256x256), descartando as bordas.
	effectiveTile = 64
	pad           = (inputSize - effectiveTile) / 2
	effectiveOut  = effectiveTile * scale
)

// InitRuntime inicializa o ambiente onnxruntime usando a DLL localizada em
// ortLibPath. Deve ser chamado uma vez antes de New() e balanceado por
// DestroyRuntime.
func InitRuntime(ortLibPath string) error {
	ort.SetSharedLibraryPath(ortLibPath)
	if err := ort.InitializeEnvironment(); err != nil {
		return fmt.Errorf("falha ao inicializar onnxruntime: %w", err)
	}
	return nil
}

// DestroyRuntime encerra o ambiente onnxruntime (libera a DLL).
func DestroyRuntime() error {
	return ort.DestroyEnvironment()
}

// FindLibrary localiza a onnxruntime.dll na seguinte ordem: variável de
// ambiente ONNXRUNTIME_SHARED_LIBRARY_PATH, diretório do executável e
// diretório de trabalho atual.
func FindLibrary() (string, error) {
	candidates := []string{}

	if p := os.Getenv("ONNXRUNTIME_SHARED_LIBRARY_PATH"); p != "" {
		candidates = append(candidates, p)
	}

	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), dllName()))
	}

	// Diretório de trabalho atual (útil em desenvolvimento, ex.: raiz do projeto)
	candidates = append(candidates, dllName())

	for _, p := range candidates {
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return p, nil
		}
	}

	// Fallback autocontido: extrai as DLLs embutidas no binário para um temp.
	if p, err := ortdll.Extract(); err == nil {
		return p, nil
	}

	return "", fmt.Errorf("onnxruntime.dll não encontrada (procurei: %v). "+
		"Copie a DLL ao lado do executável ou defina ONNXRUNTIME_SHARED_LIBRARY_PATH", candidates)
}

// workerSession reúne uma sessão de inferência e seus tensores. Cada worker
// tem sua própria sessão para que tiles possam ser processados em paralelo.
type workerSession struct {
	inputTensor  *ort.Tensor[uint8]
	outputTensor *ort.Tensor[uint8]
	session      *ort.AdvancedSession
}

// RealESRGAN encapsula um conjunto de sessões do modelo carregadas em memória.
// Uma instância deve ser criada com New() e liberada com Close() quando o
// processamento terminar.
type RealESRGAN struct {
	workers  []*workerSession
	modelDir string
	usingGPU bool
}

// New cria as sessões de inferência (DirectML se disponível) e materializa o
// modelo embutido em disco. Exige que InitRuntime tenha sido chamado antes.
func New() (*RealESRGAN, error) {
	return NewWithWorkers(defaultWorkerCount())
}

// NewWithWorkers é como New, mas permite controlar quantas sessões paralelas
// serão criadas. Use workers <= 1 para execução estritamente serial.
func NewWithWorkers(workers int) (*RealESRGAN, error) {
	if !ort.IsInitialized() {
		return nil, fmt.Errorf("onnxruntime não inicializado; chame InitRuntime antes de New")
	}
	if workers < 1 {
		workers = 1
	}

	dir, err := materializeModel()
	if err != nil {
		return nil, err
	}
	onnxPath := filepath.Join(dir, modelOnnx)

	opts, usingGPU, err := newSessionOptions()
	if err != nil {
		os.RemoveAll(dir)
		return nil, fmt.Errorf("falha ao criar opções de sessão: %w", err)
	}
	defer opts.Destroy()

	r := &RealESRGAN{modelDir: dir, usingGPU: usingGPU}
	for i := 0; i < workers; i++ {
		ws, err := createWorkerSession(onnxPath, opts)
		if err != nil {
			r.Close()
			return nil, err
		}
		r.workers = append(r.workers, ws)
	}
	return r, nil
}

// createWorkerSession cria uma sessão e os tensores de entrada/saída a partir
// das opções fornecidas.
func createWorkerSession(onnxPath string, opts *ort.SessionOptions) (*workerSession, error) {
	inputShape := ort.NewShape(1, 3, inputSize, inputSize)
	inputTensor, err := ort.NewEmptyTensor[uint8](inputShape)
	if err != nil {
		return nil, fmt.Errorf("falha ao criar tensor de entrada: %w", err)
	}

	outputShape := ort.NewShape(1, 3, outputSize, outputSize)
	outputTensor, err := ort.NewEmptyTensor[uint8](outputShape)
	if err != nil {
		inputTensor.Destroy()
		return nil, fmt.Errorf("falha ao criar tensor de saída: %w", err)
	}

	session, err := ort.NewAdvancedSession(
		onnxPath,
		[]string{"image"},
		[]string{"upscaled_image"},
		[]ort.Value{inputTensor},
		[]ort.Value{outputTensor},
		opts,
	)
	if err != nil {
		inputTensor.Destroy()
		outputTensor.Destroy()
		return nil, fmt.Errorf("falha ao carregar sessão do modelo: %w", err)
	}

	return &workerSession{
		inputTensor:  inputTensor,
		outputTensor: outputTensor,
		session:      session,
	}, nil
}

// newSessionOptions monta as opções de sessão com DirectML (GPU) quando
// suportado, caindo para CPU caso contrário. Retorna também se a GPU está ativa.
func newSessionOptions() (*ort.SessionOptions, bool, error) {
	opts, err := ort.NewSessionOptions()
	if err != nil {
		return nil, false, err
	}

	// Otimiza o grafo e usa todos os núcleos da CPU (relevante no fallback).
	if err := opts.SetGraphOptimizationLevel(ort.GraphOptimizationLevelEnableAll); err != nil {
		return nil, false, err
	}
	opts.SetIntraOpNumThreads(runtime.NumCPU())

	// Tenta habilitar a GPU via DirectML; se não houver suporte, segue em CPU.
	usingGPU := false
	if err := opts.AppendExecutionProviderDirectML(0); err == nil {
		usingGPU = true
	}
	return opts, usingGPU, nil
}

// Close libera as sessões, os tensores e remove o modelo materializado em disco.
func (r *RealESRGAN) Close() error {
	var firstErr error
	for _, ws := range r.workers {
		if ws == nil {
			continue
		}
		if ws.session != nil {
			if err := ws.session.Destroy(); err != nil && firstErr == nil {
				firstErr = err
			}
			ws.session = nil
		}
		if ws.inputTensor != nil {
			if err := ws.inputTensor.Destroy(); err != nil && firstErr == nil {
				firstErr = err
			}
			ws.inputTensor = nil
		}
		if ws.outputTensor != nil {
			if err := ws.outputTensor.Destroy(); err != nil && firstErr == nil {
				firstErr = err
			}
			ws.outputTensor = nil
		}
	}
	r.workers = nil

	if r.modelDir != "" {
		if err := os.RemoveAll(r.modelDir); err != nil && firstErr == nil {
			firstErr = err
		}
		r.modelDir = ""
	}
	return firstErr
}

// UsingGPU informa se a inferência está sendo executada na GPU via DirectML.
func (r *RealESRGAN) UsingGPU() bool {
	return r.usingGPU
}

// Workers devolve quantas sessões paralelas (e, portanto, quantas páginas podem
// ser processadas ao mesmo tempo com tensores isolados) estão disponíveis.
func (r *RealESRGAN) Workers() int {
	return len(r.workers)
}

// Upscale aplica a super-resolução 4x sobre a imagem, processando os tiles
// (área efetiva menor com padding) em paralelo (um worker por sessão) e retorna
// uma imagem com 4x as dimensões da entrada.
func (r *RealESRGAN) Upscale(img image.Image) (image.Image, error) {
	b := img.Bounds()
	srcW, srcH := b.Dx(), b.Dy()
	if srcW == 0 || srcH == 0 {
		return nil, fmt.Errorf("imagem vazia")
	}

	outW, outH := srcW*scale, srcH*scale
	out := image.NewRGBA(image.Rect(0, 0, outW, outH))

	cols := (srcW + effectiveTile - 1) / effectiveTile
	rows := (srcH + effectiveTile - 1) / effectiveTile

	workers := len(r.workers)
	if workers < 1 {
		workers = 1
	}

	tileCount := rows * cols
	errCh := make(chan error, workers)
	var wg sync.WaitGroup

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			wrk := r.workers[w]
			for t := w; t < tileCount; t += workers {
				tx := t % cols
				ty := t / cols
				if err := runTile(wrk, img, out, tx, ty, srcW, srcH); err != nil {
					errCh <- err
					return
				}
			}
		}(w)
	}

	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			return nil, err
		}
	}

	return out, nil
}

// UpscaleOne aplica a super-resolução 4x sobre uma única página usando somente a
// sessão workerIndex, em série sobre os tiles (reusando os tensores dessa sessão).
// Permite processar várias páginas em paralelo, desde que cada goroutine use um
// workerIndex distinto (os tensores são isolados por sessão, evitando corrupção
// de memória ao compartilhar entrada/saída entre threads).
func (r *RealESRGAN) UpscaleOne(img image.Image, workerIndex int) (image.Image, error) {
	if workerIndex < 0 || workerIndex >= len(r.workers) {
		return nil, fmt.Errorf("workerIndex %d fora do intervalo [0,%d)", workerIndex, len(r.workers))
	}
	wrk := r.workers[workerIndex]

	b := img.Bounds()
	srcW, srcH := b.Dx(), b.Dy()
	if srcW == 0 || srcH == 0 {
		return nil, fmt.Errorf("imagem vazia")
	}

	out := image.NewRGBA(image.Rect(0, 0, srcW*scale, srcH*scale))
	cols := (srcW + effectiveTile - 1) / effectiveTile
	rows := (srcH + effectiveTile - 1) / effectiveTile

	for ty := 0; ty < rows; ty++ {
		for tx := 0; tx < cols; tx++ {
			if err := runTile(wrk, img, out, tx, ty, srcW, srcH); err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

// runTile preenche um tile de entrada com a área efetiva (effectiveTile) no
// centro e padding replicado nas bordas, executa a inferência e grava apenas a
// região central 4x correspondente na imagem de saída. Descartar as bordas
// elimina os artefatos típicos de tile e dá mais contexto ao modelo. Cada tile
// escreve em pixels distintos, então é seguro rodar em paralelo.
func runTile(wrk *workerSession, img image.Image, out *image.RGBA, tx, ty, srcW, srcH int) error {
	x0 := tx * effectiveTile
	y0 := ty * effectiveTile

	ssIn := inputSize * inputSize
	ssOut := outputSize * outputSize

	// Tensores são criados uma única vez por sessão (NewWithWorkers) e apenas
	// sobrescritos aqui via GetData(), evitando alocação/GC a cada tile.
	inData := wrk.inputTensor.GetData()

	// Preenche o tile de entrada (NCHW, uint8 0-255): a região central
	// [pad, pad+effectiveTile) recebe pixels da fonte; as bordas são replicadas
	// (clamp) para dar contexto ao modelo.
	for py := 0; py < inputSize; py++ {
		sy := y0 + (py - pad)
		if sy < 0 {
			sy = 0
		} else if sy >= srcH {
			sy = srcH - 1
		}
		for px := 0; px < inputSize; px++ {
			sx := x0 + (px - pad)
			if sx < 0 {
				sx = 0
			} else if sx >= srcW {
				sx = srcW - 1
			}
			r_, g, b, _ := img.At(sx, sy).RGBA()
			idx := py*inputSize + px
			inData[idx] = uint8(r_ >> 8)
			inData[ssIn+idx] = uint8(g >> 8)
			inData[2*ssIn+idx] = uint8(b >> 8)
		}
	}

	if err := wrk.session.Run(); err != nil {
		return fmt.Errorf("erro na inferência (tile %d,%d): %w", tx, ty, err)
	}
	outData := wrk.outputTensor.GetData()

	// Região efetiva do tile que existe dentro da imagem fonte.
	validW := effectiveTile
	if x0+validW > srcW {
		validW = srcW - x0
	}
	validH := effectiveTile
	if y0+validH > srcH {
		validH = srcH - y0
	}

	// A saída da área efetiva está na região central da saída do modelo
	// ([pad*scale, pad*scale+effectiveOut)). Percorre a região em resolução
	// cheia (passo 1, NÃO scale), pois cada pixel do tensor de saída é um pixel
	// final; amostrar com passo `scale` deixaria linhas pretas entre pixels
	// (artefato de grade/scanline).
	for oy := 0; oy < validH*scale; oy++ {
		for ox := 0; ox < validW*scale; ox++ {
			idx := (pad*scale+oy)*outputSize + (pad*scale + ox)
			out.SetRGBA(
				x0*scale+ox,
				y0*scale+oy,
				color.RGBA{R: outData[idx], G: outData[ssOut+idx], B: outData[2*ssOut+idx], A: 255},
			)
		}
	}
	return nil
}

// defaultWorkerCount devolve o número de sessões paralelas a criar: número de
// núcleos da CPU limitado a um teto para não multiplicar o uso de memória
// (cada sessão carrega uma cópia do modelo).
func defaultWorkerCount() int {
	n := runtime.NumCPU()
	if n < 1 {
		n = 1
	}
	if n > 4 {
		n = 4
	}
	return n
}

// materializeModel grava o modelo embutido (grafo + pesos externos) em um
// diretório temporário, pois o ONNX precisa resolver o arquivo de dados externo
// no mesmo diretório do .onnx. Retorna o caminho do diretório criado.
func materializeModel() (string, error) {
	dir, err := os.MkdirTemp(os.TempDir(), "esrgan_model_*")
	if err != nil {
		return "", fmt.Errorf("falha ao criar diretório temporário do modelo: %w", err)
	}

	if err := os.WriteFile(filepath.Join(dir, modelOnnx), modeldata.Onnx(), 0644); err != nil {
		os.RemoveAll(dir)
		return "", fmt.Errorf("falha ao gravar %s: %w", modelOnnx, err)
	}
	if err := os.WriteFile(filepath.Join(dir, modelData), modeldata.Data(), 0644); err != nil {
		os.RemoveAll(dir)
		return "", fmt.Errorf("falha ao gravar %s: %w", modelData, err)
	}
	return dir, nil
}

// dllName retorna o nome da biblioteca conforme a plataforma.
func dllName() string {
	if os.PathSeparator == '/' {
		return "libonnxruntime.so"
	}
	return "onnxruntime.dll"
}
