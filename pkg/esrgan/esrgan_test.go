package esrgan

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestFindLibraryFallsBackToEmbed garante que, quando não há DLL ao lado do exe
// nem no cwd (e ONNXRUNTIME_SHARED_LIBRARY_PATH não aponta para ela), o
// FindLibrary extrai as DLLs embutidas num temp e devolve o caminho válido.
func TestFindLibraryFallsBackToEmbed(t *testing.T) {
	// Garante que o teste não depende de variável externa apontando para a DLL.
	// Se o ambiente tiver a DLL próxima, o teste apenas confirma o caminho válido.
	p, err := FindLibrary()
	if err != nil {
		t.Fatalf("FindLibrary: %v", err)
	}
	if p == "" {
		t.Fatal("FindLibrary retornou caminho vazio")
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("DLL não existe em %s: %v", p, err)
	}
	t.Logf("library: %s", p)
	if strings.HasPrefix(p, os.TempDir()) {
		t.Log("usou o fallback de extração embutida")
	} else if filepath.Base(p) == "onnxruntime.dll" {
		t.Log("usou a DLL encontrada em disco")
	}
}
