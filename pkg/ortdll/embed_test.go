package ortdll

import (
	"os"
	"path/filepath"
	"testing"
)

// TestExtract garante que a extração materializa a DLL principal e a auxiliar
// (providers_shared) num diretório temporário.
func TestExtract(t *testing.T) {
	p, err := Extract()
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("onnxruntime.dll não extraída em %s: %v", p, err)
	}
	dir := filepath.Dir(p)
	if _, err := os.Stat(filepath.Join(dir, "onnxruntime_providers_shared.dll")); err != nil {
		t.Errorf("onnxruntime_providers_shared.dll ausente: %v", err)
	}
	// Idempotente: segunda chamada retorna o mesmo caminho.
	p2, err := Extract()
	if err != nil {
		t.Fatalf("Extract 2: %v", err)
	}
	if p != p2 {
		t.Errorf("extração não idempotente: %s vs %s", p, p2)
	}
}
