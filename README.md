# CBZ Converter

Aplicativo desktop (Wails v3) para **ajustar a resolução das páginas de um arquivo
CBZ** (comic book) e **regenerá-lo**, aplicando super-resolução com
**Real-ESRGAN** quando uma página precisa ser ampliada (upscaling).

Backend em **Go**, frontend em **React + TypeScript** (Vite + Tailwind v4).

## Fluxo da aplicação

1. O usuário informa a **largura** e **altura** alvo (default `1080x1920`).
2. Seleciona um arquivo `.cbz`.
3. Ao clicar em **Processar e Salvar CBZ**, o backend:
   - Carrega o modelo Real-ESRGAN (sessão ONNX) — criada ao iniciar o processo e
     destruída ao terminar;
   - Extrai o CBZ, redimensiona cada página;
   - Se a página for **menor** que a resolução alvo, aplica **Real-ESRGAN (4x)**
     antes de ajustar à resolução;
   - Reempacota tudo em um novo CBZ (JPEG q90, páginas renomeadas `001.jpg`, ...).
4. O usuário escolhe onde salvar o `.cbz` gerado.

## Pré-requisitos

- **Go 1.25+**
- **Node.js** + npm
- **Wails v3 CLI** (`go install github.com/wailsapp/wails/v3/cmd/wails3@latest`)
- **gcc/MinGW-w64** com **cgo habilitado** — obrigatório para a inferência ONNX
  (`onnxruntime_go`). Sem isso o build falha com
  `build constraints exclude all Go files`.
- **onnxruntime.dll** (DirectML v1.24.4) + `onnxruntime_providers_shared.dll` —
  devem estar ao lado do executável (`bin/`) ou apontados por
  `ONNXRUNTIME_SHARED_LIBRARY_PATH`. O build DirectML acelera a inferência na
  **GPU** (com fallback automático para CPU).

## Setup

```powershell
# 1. Instale um toolchain C (ex.: MinGW-w64 via https://winget.run/pkg/BrechtSanders/winlibs-gcc-msvcrt)
#    e adicione o diretório bin do gcc ao PATH.

# 2. Garanta cgo habilitado (persista no ambiente):
$env:CGO_ENABLED = "1"

# 3. Instale as dependências do frontend
cd frontend; npm install; cd ..

# 4. Garanta a onnxruntime.dll (DirectML) em bin\ (baixa do NuGet)
powershell -ExecutionPolicy Bypass -File scripts\setup-onnxruntime.ps1
```

## Executando

```powershell
# Desenvolvimento (hot reload backend + frontend)
wails3 dev

# Build de produção (gera bin\cbz-converter.exe)
wails3 build
```

> Nota: por causa do `onnxruntime_go`, **todos** os builds do backend agora exigem
> cgo (`CGO_ENABLED=1` + gcc). O `build/windows/Taskfile.yml` já defaulta
> `CGO_ENABLED=1`, então `wails3 dev` e `wails3 build` funcionam sem flag extra.
> Até que o gcc esteja instalado, `go build ./pkg/...` e `wails3 build` falharão.
>
> Workaround (manifesto duplicado): `wails3 build CGO_ENABLED=1` pode falhar no
> link com `ld.exe: .rsrc merge failure: multiple non-default manifests`. Isso
> ocorre porque o driver do gcc/MinGW auto-linka um `default-manifest.o`, que
> colide com o manifesto embutido no `.syso` do Wails. Correção: esvaziar o
> `default-manifest.o` da toolchain (`mingw64\x86_64-w64-mingw32\lib\
> default-manifest.o`, com backup `.bak`). Se o erro persistir, `go clean -cache`
> antes de rebuildar. É um ajuste de máquina; só precisa refazer se o MinGW for
> reinstalado/atualizado.

## Estrutura

- `main.go` — registra o serviço e embute o frontend.
- `pkg/services/cbz_service.go` — `CBZService` (métodos expostos ao frontend:
  `UnpackCBZ`, `CleanupTempFolder`, `ProcessCBZ`, `SaveCBZ`).
- `pkg/esrgan/esrgan.go` — integração ONNX / Real-ESRGAN (sessões, DirectML/GPU,
  lifecycle e upscaling em blocos/tiles processados em paralelo).
- `pkg/real_esrgan_general_x4v3-onnx-w8a8/` — modelo embutido via `go:embed`
  (quantizado w8a8, uint8; `.onnx` + pesos externos `.data`; ambos são gravados
  em disco no mesmo diretório antes de onnxruntime carregar).
- `pkg/yolo/` — detecção de objetos (YOLOv8n ONNX embutido no pacote) para gerar
  uma máscara de proteção ao seam carving (protege personagens/objetos do corte).
- `frontend/` — UI em React/TS; `App.tsx` orquestra o fluxo e chama o backend
  via `frontend/bindings/...` (auto-gerado).
- `scripts/setup-onnxruntime.ps1` — garante a `onnxruntime.dll` em `bin/`.

## Como funciona a inferência (ONNX + Real-ESRGAN)

- O modelo é o **Real-ESRGAN general-x4-v3 quantizado w8a8** (ONNX, uint8),
  entrada fixa `1x3x128x128` → saída `1x3x512x512` (upscale 4x). A versão
  quantizada é bem menor e mais rápida que a float, sem perda visual perceptível
  em HQs/mangás.
- Imagens são processadas em **blocos (tiles)** de `64x64` efetivos centralizados
  em tensores `128x128` com padding por replicação de borda (evita artefatos nas
  divisórias); os blocos são remontados em resolução cheia nas coordenadas 4x
  correspondentes.
- A inferência usa o **execution provider DirectML** para rodar na **GPU**
  quando disponível, com fallback automático para CPU.
- Os **tensores de entrada/saída são criados uma única vez por sessão e apenas
  sobrescritos via `GetData()`** a cada tile (sem alocação/GC por tile).
- **Páginas são processadas em paralelo** (worker pool limitado ao nº de
  sessões): cada página em vôo usa uma sessão distinta (`UpscaleOne`) com
  tensores isolados, garantindo segurança de memória e paralelismo real. O zip
  é gravado em ordem, serialmente.
- O modelo (grafo `.onnx` + pesos `.data`) é **embutido no binário** via
  `go:embed` e materializado em um diretório temporário na inicialização (o
  formato de pesos externos exige o `.data` no mesmo diretório do `.onnx`).
- A sessão é **criada** quando o usuário aciona o processamento e **destruída**
  quando o processamento termina.

## Referências

- **Real-ESRGAN** — Wang et al., *Real-ESRGAN: Training Real-World Blind
  Super-Resolution with Pure Synthetic Data* ([arXiv:2107.10833](https://arxiv.org/abs/2107.10833), [GitHub](https://github.com/xinntao/Real-ESRGAN)).
- **Modelo** — [qualcomm/Real-ESRGAN-x4plus](https://huggingface.co/qualcomm/Real-ESRGAN-x4plus) (ONNX, float; pré-exportado pela Qualcomm AI Hub).
- **ONNX Runtime** — [microsoft/onnxruntime](https://github.com/microsoft/onnxruntime).
- **Wrapper Go** — [yalue/onnxruntime_go](https://github.com/yalue/onnxruntime_go)
  (carrega a `onnxruntime.dll` por `syscall` e remove a dependência de compilação C; veja os [exemplos](https://github.com/yalue/onnxruntime_go_examples)).
- **Wails v3** — [v3.wails.io](https://v3.wails.io/).