// Package yolo encapsula a detecção de objetos com o modelo yolo26n
// (Ultralytics YOLOv8n, ONNX) via onnxruntime. Detecta personagens/objetos de
// uma página para gerar uma máscara de proteção usada pelo retargeting (evita
// cortar o conteúdo).
//
// Assume que o ambiente onnxruntime já foi inicializado (ex.: por esrgan.InitRuntime
// no ProcessCBZ) — apenas as sessões/tensores são criados aqui.
package yolo

import (
	"fmt"
	"image"
	"image/color"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sync"

	ort "github.com/yalue/onnxruntime_go"
	"golang.org/x/image/draw"
)

const (
	inputSize = 640
	maxDet    = 300
	attrs     = 6 // x1, y1, x2, y2, score, class
	modelName = "yolo26n.onnx"
)

// ConfThreshold é a confiança mínima para considerar uma detecção.
var ConfThreshold = float32(0.25)

// Box é uma detecção: caixa em coordenadas da imagem original.
type Box struct {
	X1, Y1, X2, Y2 float32
	Score          float32
	Class          int
}

// Detector encapsula a sessão de inferência da detecção.
type Detector struct {
	inputTensor *ort.Tensor[float32]
	output      *ort.Tensor[float32]
	session     *ort.AdvancedSession
	modelDir    string

	// mu serializa Detect: a sessão e os tensores são compartilhados, então só
	// uma inferência por vez é segura (páginas podem rodar em paralelo).
	mu sync.Mutex
}

// New cria a sessão de inferência (DirectML se disponível). Requer que o
// ambiente onnxruntime já esteja inicializado.
func New() (*Detector, error) {
	if !ort.IsInitialized() {
		return nil, fmt.Errorf("onnxruntime não inicializado; chame esrgan.InitRuntime antes de New")
	}

	dir, err := os.MkdirTemp(os.TempDir(), "yolo_model_*")
	if err != nil {
		return nil, fmt.Errorf("falha ao criar diretório temporário do modelo: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, modelName), modeldataOnnx(), 0644); err != nil {
		os.RemoveAll(dir)
		return nil, fmt.Errorf("falha ao gravar %s: %w", modelName, err)
	}
	onnxPath := filepath.Join(dir, modelName)

	inputTensor, err := ort.NewEmptyTensor[float32](ort.NewShape(1, 3, inputSize, inputSize))
	if err != nil {
		os.RemoveAll(dir)
		return nil, fmt.Errorf("falha ao criar tensor de entrada: %w", err)
	}
	output, err := ort.NewEmptyTensor[float32](ort.NewShape(1, maxDet, attrs))
	if err != nil {
		inputTensor.Destroy()
		os.RemoveAll(dir)
		return nil, fmt.Errorf("falha ao criar tensor de saída: %w", err)
	}

	opts, err := ort.NewSessionOptions()
	if err != nil {
		inputTensor.Destroy()
		output.Destroy()
		os.RemoveAll(dir)
		return nil, err
	}
	defer opts.Destroy()
	opts.SetGraphOptimizationLevel(ort.GraphOptimizationLevelEnableAll)
	opts.SetIntraOpNumThreads(runtime.NumCPU())
	opts.AppendExecutionProviderDirectML(0)

	session, err := ort.NewAdvancedSession(
		onnxPath,
		[]string{"images"},
		[]string{"output0"},
		[]ort.Value{inputTensor},
		[]ort.Value{output},
		opts,
	)
	if err != nil {
		inputTensor.Destroy()
		output.Destroy()
		os.RemoveAll(dir)
		return nil, fmt.Errorf("falha ao carregar sessão do YOLO: %w", err)
	}

	return &Detector{
		inputTensor: inputTensor,
		output:      output,
		session:     session,
		modelDir:    dir,
	}, nil
}

// Close libera a sessão e remove o modelo materializado.
func (d *Detector) Close() error {
	var firstErr error
	if d.session != nil {
		if err := d.session.Destroy(); err != nil && firstErr == nil {
			firstErr = err
		}
		d.session = nil
	}
	if d.inputTensor != nil {
		if err := d.inputTensor.Destroy(); err != nil && firstErr == nil {
			firstErr = err
		}
		d.inputTensor = nil
	}
	if d.output != nil {
		if err := d.output.Destroy(); err != nil && firstErr == nil {
			firstErr = err
		}
		d.output = nil
	}
	if d.modelDir != "" {
		if err := os.RemoveAll(d.modelDir); err != nil && firstErr == nil {
			firstErr = err
		}
		d.modelDir = ""
	}
	return firstErr
}

// Detect faz o letterbox, roda a inferência e devolve as caixas (já com NMS
// aplicado pelo modelo) nas coordenadas da imagem original.
func (d *Detector) Detect(img image.Image) ([]Box, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	srcW, srcH := img.Bounds().Dx(), img.Bounds().Dy()
	if srcW <= 0 || srcH <= 0 {
		return nil, fmt.Errorf("imagem vazia")
	}
	work, scale, padX, padY := letterbox(img)

	// Preenche a entrada NCHW: RGB 0-1.
	inData := d.inputTensor.GetData()
	ss := inputSize * inputSize
	b := work.Bounds()
	for y := 0; y < inputSize; y++ {
		for x := 0; x < inputSize; x++ {
			c := work.RGBAAt(b.Min.X+x, b.Min.Y+y)
			idx := y*inputSize + x
			inData[idx] = float32(c.R) / 255.0
			inData[ss+idx] = float32(c.G) / 255.0
			inData[2*ss+idx] = float32(c.B) / 255.0
		}
	}

	if err := d.session.Run(); err != nil {
		return nil, fmt.Errorf("erro na inferência YOLO: %w", err)
	}

	out := d.output.GetData()
	var boxes []Box
	for r := 0; r < maxDet; r++ {
		base := r * attrs
		score := out[base+4]
		if score < ConfThreshold {
			continue
		}
		x1 := (out[base] - float32(padX)) / scale
		y1 := (out[base+1] - float32(padY)) / scale
		x2 := (out[base+2] - float32(padX)) / scale
		y2 := (out[base+3] - float32(padY)) / scale
		if x2 <= x1 || y2 <= y1 {
			continue
		}
		boxes = append(boxes, Box{
			X1: x1, Y1: y1, X2: x2, Y2: y2,
			Score: score,
			Class: int(out[base+5]),
		})
	}
	return boxes, nil
}

// BuildMask cria uma imagem B&W (gray) do tamanho width x height onde o interior
// das caixas (dilatadas por margin px) é branco (protegido) e o resto é preto.
func (d *Detector) BuildMask(width, height, margin int, boxes []Box) image.Image {
	mask := image.NewGray(image.Rect(0, 0, width, height))
	for _, b := range boxes {
		x1 := clamp(int(math.Floor(float64(b.X1)))-margin, 0, width-1)
		y1 := clamp(int(math.Floor(float64(b.Y1)))-margin, 0, height-1)
		x2 := clamp(int(math.Ceil(float64(b.X2)))+margin, 0, width-1)
		y2 := clamp(int(math.Ceil(float64(b.Y2)))+margin, 0, height-1)
		for y := y1; y <= y2; y++ {
			for x := x1; x <= x2; x++ {
				mask.SetGray(x, y, color.Gray{Y: 255})
			}
		}
	}
	return mask
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// letterbox redimensiona a imagem para caber em 640x640 preservando o aspect
// ratio e centraliza num canvas preto. Retorna o canvas, a escala aplicada e os
// paddings (para converter as caixas de volta às coordenadas originais).
func letterbox(img image.Image) (canvas *image.RGBA, scale float32, padX, padY int) {
	srcW, srcH := img.Bounds().Dx(), img.Bounds().Dy()
	r := float64(inputSize) / math.Max(float64(srcW), float64(srcH))
	newW := int(math.Round(float64(srcW) * r))
	newH := int(math.Round(float64(srcH) * r))
	if newW < 1 {
		newW = 1
	}
	if newH < 1 {
		newH = 1
	}
	padX = (inputSize - newW) / 2
	padY = (inputSize - newH) / 2

	resized := image.NewRGBA(image.Rect(0, 0, newW, newH))
	draw.ApproxBiLinear.Scale(resized, resized.Bounds(), img, img.Bounds(), draw.Over, nil)

	canvas = image.NewRGBA(image.Rect(0, 0, inputSize, inputSize))
	draw.Draw(canvas, canvas.Bounds(), image.Black, image.Point{}, draw.Src)
	draw.Draw(canvas, image.Rect(padX, padY, padX+newW, padY+newH), resized, image.Point{}, draw.Src)
	return canvas, float32(r), padX, padY
}
