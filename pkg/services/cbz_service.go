package services

import (
	"archive/zip"
	"bytes"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"cbz-converter/pkg/esrgan"
	"cbz-converter/pkg/retarget"
	"cbz-converter/pkg/yolo"
	"github.com/wailsapp/wails/v3/pkg/application"

	// Decoders extras para formatos de imagem suportados (webp, bmp, tiff, gif)
	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/tiff"
	_ "golang.org/x/image/webp"
)

// CBZService centraliza a lógica de manipular arquivos CBZ
type CBZService struct{}

// ExtractedCBZ contém o resultado da extração para retornar ao frontend
type ExtractedCBZ struct {
	TempFolder string   `json:"tempFolder"`
	PageCount  int      `json:"pageCount"`
	Files      []string `json:"files"`
}

// CBZProgress descreve o andamento do processamento emitido ao frontend via
// o evento "cbz:progress". A porcentagem é calculada com base nas páginas
// já processadas em relação ao total.
type CBZProgress struct {
	Percentage float64 `json:"percentage"`
	Page       int     `json:"page"`
	Total      int     `json:"total"`
	Stage      string  `json:"stage"`
	Message    string  `json:"message"`
}

// emitProgress publica um evento de progresso para a interface. É chamado de
// dentro de ProcessCBZ; falhas de emissão não abortam o processamento.
func (s *CBZService) emitProgress(p CBZProgress) {
	if app := application.Get(); app != nil {
		app.Event.Emit("cbz:progress", p)
	}
}

// UnpackCBZ recebe o nome e os bytes do arquivo enviado pelo frontend,
// extrai na pasta temporária do sistema e retorna as informações dos arquivos descompactados.
func (s *CBZService) UnpackCBZ(fileName string, fileData []byte) (*ExtractedCBZ, error) {
	// 1. Cria uma subpasta temporária única na pasta Temp do sistema
	tempDir, err := os.MkdirTemp(os.TempDir(), "cbz_process_*")
	if err != nil {
		return nil, fmt.Errorf("falha ao criar pasta temporária: %w", err)
	}

	// 2. Abre o slice de bytes como um arquivo ZIP na memória
	zipReader, err := zip.NewReader(bytes.NewReader(fileData), int64(len(fileData)))
	if err != nil {
		os.RemoveAll(tempDir) // Limpa em caso de erro
		return nil, fmt.Errorf("arquivo inválido ou corrompido: %w", err)
	}

	var extractedFiles []string

	// 3. Iterar e extrair os arquivos do arquivo ZIP/CBZ
	for _, file := range zipReader.File {
		path := filepath.Join(tempDir, file.Name)

		// Proteção contra vulnerabilidade Zip Slip
		if !strings.HasPrefix(path, filepath.Clean(tempDir)+string(os.PathSeparator)) {
			os.RemoveAll(tempDir)
			return nil, fmt.Errorf("caminho inválido detectado no arquivo: %s", file.Name)
		}

		// Se for diretório, apenas cria a estrutura de pastas
		if file.FileInfo().IsDir() {
			os.MkdirAll(path, os.ModePerm)
			continue
		}

		// Garante que o diretório pai do arquivo exista
		if err := os.MkdirAll(filepath.Dir(path), os.ModePerm); err != nil {
			os.RemoveAll(tempDir)
			return nil, fmt.Errorf("erro ao criar diretório para %s: %w", file.Name, err)
		}

		// Grava o conteúdo no disco
		outFile, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, file.Mode())
		if err != nil {
			os.RemoveAll(tempDir)
			return nil, fmt.Errorf("erro ao criar arquivo %s: %w", path, err)
		}

		rc, err := file.Open()
		if err != nil {
			outFile.Close()
			os.RemoveAll(tempDir)
			return nil, fmt.Errorf("erro ao abrir conteúdo de %s: %w", file.Name, err)
		}

		_, err = io.Copy(outFile, rc)
		outFile.Close()
		rc.Close()

		if err != nil {
			os.RemoveAll(tempDir)
			return nil, fmt.Errorf("erro ao extrair %s: %w", file.Name, err)
		}

		// Filtra apenas imagens válidas para listar no retorno
		if isImageFile(file.Name) {
			extractedFiles = append(extractedFiles, path)
		}
	}

	// Ordena os nomes das imagens para manter a sequência correta das páginas
	sort.Strings(extractedFiles)

	return &ExtractedCBZ{
		TempFolder: tempDir,
		PageCount:  len(extractedFiles),
		Files:      extractedFiles,
	}, nil
}

// CleanupTempFolder remove a pasta temporária do disco quando o processamento for concluído
func (s *CBZService) CleanupTempFolder(folderPath string) error {
	// Verifica se a pasta pertence ao Temp do sistema por segurança antes de deletar
	if !strings.HasPrefix(filepath.Clean(folderPath), filepath.Clean(os.TempDir())) {
		return fmt.Errorf("tentativa não autorizada de excluir fora do diretório temporário")
	}

	return os.RemoveAll(folderPath)
}

// ProcessCBZ recebe o nome e os bytes do CBZ enviado pelo frontend, redimensiona
// todas as imagens para a largura/altura informadas e retorna os bytes do novo CBZ.
func (s *CBZService) ProcessCBZ(fileName string, fileData []byte, width int, height int) ([]byte, error) {
	if width <= 0 || height <= 0 {
		return nil, fmt.Errorf("largura e altura devem ser maiores que zero")
	}

	// Localiza a DLL e inicializa o ambiente onnxruntime. O modelo é carregado
	// aqui (ao ativar o processamento) e liberado somente ao final do método.
	ortLib, err := esrgan.FindLibrary()
	if err != nil {
		return nil, err
	}
	s.emitProgress(CBZProgress{Stage: "Preparando", Message: "Carregando onnxruntime..."})
	if err := esrgan.InitRuntime(ortLib); err != nil {
		return nil, err
	}
	defer esrgan.DestroyRuntime()

	esr, err := esrgan.New()
	if err != nil {
		return nil, err
	}
	defer esr.Close()
	s.emitProgress(CBZProgress{Stage: "Preparando", Message: "Modelo Real-ESRGAN carregado"})
	mode := "CPU"
	if esr.UsingGPU() {
		mode = "GPU (DirectML)"
	}
	s.emitProgress(CBZProgress{Stage: "Preparando", Message: "Inferência em " + mode})

	// Detector YOLO (opcional): identifica personagens/objetos para gerar uma
	// máscara de proteção ao retargeting. Se falhar, segue sem máscara.
	var det *yolo.Detector
	if d, err := yolo.New(); err == nil {
		det = d
		defer det.Close()
		s.emitProgress(CBZProgress{Stage: "Preparando", Message: "Detector YOLO carregado"})
	} else {
		s.emitProgress(CBZProgress{Stage: "Preparando", Message: "Detector YOLO indisponível — sem máscara de proteção"})
	}

	// Reutiliza a extração para obter as páginas na pasta temporária
	extracted, err := s.UnpackCBZ(fileName, fileData)
	if err != nil {
		return nil, err
	}
	defer s.CleanupTempFolder(extracted.TempFolder)

	total := len(extracted.Files)
	if total == 0 {
		return nil, fmt.Errorf("nenhuma imagem encontrada no arquivo CBZ")
	}
	s.emitProgress(CBZProgress{
		Stage:   "Extraindo",
		Message: fmt.Sprintf("%d páginas extraídas", total),
		Total:   total,
	})

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	results := make([]image.Image, total)

	// Worker pool de sessões do modelo: cada página em vôo ocupa uma sessão
	// distinta (tensores isolados via UpscaleOne), garantindo segurança de
	// memória e paralelismo real entre páginas.
	workerCount := esr.Workers()
	if workerCount < 1 {
		workerCount = 1
	}
	free := make(chan int, workerCount)
	for i := 0; i < workerCount; i++ {
		free <- i
	}
	sem := make(chan struct{}, workerCount)

	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error
	var completed int

	start := time.Now()

	for i, path := range extracted.Files {
		wg.Add(1)
		go func(i int, path string) {
			defer wg.Done()

			sem <- struct{}{}
			defer func() { <-sem }()

			mu.Lock()
			aborted := firstErr != nil
			mu.Unlock()
			if aborted {
				return
			}

			img, err := decodeImage(path)
			if err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				mu.Unlock()
				return
			}

			var resized image.Image
			action := "ajustando resolução"
			srcW, srcH := img.Bounds().Dx(), img.Bounds().Dy()

			// Máscara de proteção (opcional): detecta personagens/objetos e marca
			// as regiões que o retargeting deve preservar.
			var mask image.Image
			if det != nil {
				if boxes, err := det.Detect(img); err == nil {
					mask = det.BuildMask(srcW, srcH, 6, boxes)
				}
			}

			if width > srcW || height > srcH {
				// Resolução alvo maior que a do arquivo: Real-ESRGAN (4x) para
				// upscaling e depois retargeting para atingir a resolução final.
				action = "melhorando qualidade"
				w := <-free
				upscaled, err := esr.UpscaleOne(img, w)
				free <- w
				if err != nil {
					mu.Lock()
					if firstErr == nil {
						firstErr = err
					}
					mu.Unlock()
					return
				}
				resized = retarget.Fit(upscaled, mask, width, height)
			} else {
				// Resolução alvo menor ou igual à do arquivo: retargeting direto.
				resized = retarget.Fit(img, mask, width, height)
			}

			results[i] = resized

			// Progresso global (páginas concluídas / total), independente da ordem
			// de conclusão, já que o processamento é paralelo. A porcentagem é
			// monotônica e reflete o total de páginas.
			mu.Lock()
			completed++
			done := completed
			mu.Unlock()

			percentage := float64(done) / float64(total) * 100
			eta := estimateRemaining(start, done, total)
			s.emitProgress(CBZProgress{
				Percentage: percentage,
				Page:       i + 1,
				Total:      total,
				Stage:      "Processando páginas",
				Message:    fmt.Sprintf("Página %d de %d — %s · tempo restante: %s", i+1, total, action, eta),
			})
		}(i, path)
	}

	wg.Wait()

	if firstErr != nil {
		zw.Close()
		return nil, firstErr
	}

	// Grava as páginas no CBZ em ordem (serial; o zip não é thread-safe).
	for i, resized := range results {
		if resized == nil {
			zw.Close()
			return nil, fmt.Errorf("página %d não foi processada", i+1)
		}
		entry := fmt.Sprintf("%03d.jpg", i+1)
		w, err := zw.Create(entry)
		if err != nil {
			zw.Close()
			return nil, fmt.Errorf("erro ao criar entrada %s: %w", entry, err)
		}
		if err := jpeg.Encode(w, resized, &jpeg.Options{Quality: 90}); err != nil {
			zw.Close()
			return nil, fmt.Errorf("erro ao codificar página %s: %w", entry, err)
		}
	}

	if err := zw.Close(); err != nil {
		return nil, fmt.Errorf("erro ao fechar o arquivo CBZ: %w", err)
	}

	s.emitProgress(CBZProgress{
		Percentage: 100,
		Page:       total,
		Total:      total,
		Stage:      "Concluído",
		Message:    "Gerando arquivo CBZ",
	})

	return buf.Bytes(), nil
}

// SaveCBZ grava os bytes do CBZ processado no caminho escolhido pelo usuário
func (s *CBZService) SaveCBZ(outputPath string, fileData []byte) error {
	return os.WriteFile(outputPath, fileData, 0644)
}

// decodeImage lê e decodifica uma imagem do disco
func decodeImage(path string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("erro ao abrir %s: %w", path, err)
	}
	defer f.Close()

	img, _, err := image.Decode(f)
	if err != nil {
		return nil, fmt.Errorf("erro ao decodificar %s: %w", path, err)
	}
	return img, nil
}

// estimateRemaining estima o tempo restante com base na taxa média de páginas
// concluídas desde o início do processamento (REST = (total-done) * elapsed/done).
func estimateRemaining(start time.Time, done, total int) string {
	if done <= 0 {
		return "calculando..."
	}
	elapsed := time.Since(start).Seconds()
	rate := elapsed / float64(done)
	remaining := time.Duration(float64(total-done) * rate * float64(time.Second))
	return formatDuration(remaining)
}

// formatDuration formata uma duração em "Xm Ys" ou "Ys".
func formatDuration(d time.Duration) string {
	s := int(d.Seconds())
	if s < 0 {
		s = 0
	}
	if s < 60 {
		return fmt.Sprintf("%ds", s)
	}
	return fmt.Sprintf("%dm %ds", s/60, s%60)
}

// Auxiliar para identificar formatos de imagens suportados
func isImageFile(filename string) bool {
	ext := strings.ToLower(filepath.Ext(filename))
	switch ext {
	case ".jpg", ".jpeg", ".png", ".webp", ".gif", ".bmp":
		return true
	default:
		return false
	}
}
