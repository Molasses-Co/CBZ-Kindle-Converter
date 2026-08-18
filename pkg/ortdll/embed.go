// Package ortdll embute as DLLs do onnxruntime (build DirectML) no binário para
// que o executável seja autocontido: na primeira inicialização elas são
// extraídas para um diretório temporário e o caminho é usado em
// ort.SetSharedLibraryPath. Isso evita depender de DLLs soltas ao lado do exe.
package ortdll

import (
	"embed"
	"os"
	"path/filepath"
	"sync"
)

//go:embed onnxruntime.dll onnxruntime_providers_shared.dll
var dlls embed.FS

const mainDLL = "onnxruntime.dll"

var (
	mu        sync.Mutex
	extracted string
)

// Extract materializa as DLLs embutidas num diretório temporário (uma única
// vez) e retorna o caminho da onnxruntime.dll. A DLL principal depende de
// onnxruntime_providers_shared.dll no mesmo diretório, então ambas são escritas.
func Extract() (string, error) {
	mu.Lock()
	defer mu.Unlock()

	if extracted != "" {
		if _, err := os.Stat(extracted); err == nil {
			return extracted, nil
		}
	}

	dir, err := os.MkdirTemp("", "ortdll-*")
	if err != nil {
		return "", err
	}
	for _, name := range []string{mainDLL, "onnxruntime_providers_shared.dll"} {
		data, err := dlls.ReadFile(name)
		if err != nil {
			return "", err
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o755); err != nil {
			return "", err
		}
	}

	extracted = filepath.Join(dir, mainDLL)
	return extracted, nil
}
