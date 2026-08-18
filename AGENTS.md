# AGENTS.md

Wails v3 desktop app: Go backend + React/TypeScript (Vite + Tailwind v4) frontend.
Builds for Windows. Run everything from the repo root.

## Commands

- Dev (hot reload backend + frontend): `wails3 dev`
- Full production build (regenerates bindings, builds frontend, produces `bin/cbz-converter.exe`): `wails3 build`
- Frontend typecheck + build only: `cd frontend && npm run build` (runs `tsc && vite build`)
- Regenerate TS bindings from Go services: `wails3 generate bindings -ts -i`
- Backend compile check: `go build ./pkg/... .`

Gotchas:
- `go build ./...` FAILS on a stale `build/ios` artifact ("function main is undeclared in the main package"). Ignore it; target packages with `go build ./pkg/... .` instead.
- **cgo REQUIRED now**: `pkg/esrgan` imports `onnxruntime_go` (cgo-only). Building the backend needs `CGO_ENABLED=1` AND gcc/MinGW on PATH. With `CGO_ENABLED=0` (default on this machine) the build fails with `build constraints exclude all Go files`. Any Go build/work requires a working cgo toolchain.
- `build/windows/Taskfile.yml` now defaults `CGO_ENABLED` to `"1"`, so plain `wails3 build` / `wails3 dev` work without passing the flag (the WinLibs gcc is on PATH).
- Backend compile check that works only with cgo on: `go build ./pkg/... .` (skips `build/ios`).
- `onnxruntime.dll` (DirectML build v1.24.4) + `onnxruntime_providers_shared.dll` must sit next to the exe (`bin/`) or set `ONNXRUNTIME_SHARED_LIBRARY_PATH`. Reproduce them with `scripts/setup-onnxruntime.ps1` (downloads `Microsoft.ML.OnnxRuntime.DirectML` from NuGet). This DirectML build requires `onnxruntime_go` **v1.24.0** (API 24) — do NOT bump to a newer version without also bumping the DLL, or the API-version mismatch breaks init.
- **cgo + Wails manifest conflict**: `wails3 build CGO_ENABLED=1` fails the link with `ld.exe: .rsrc merge failure: multiple non-default manifests`. Cause: the gcc/MinGW driver auto-links its `default-manifest.o` (one manifest) which collides with the app manifest embedded in the `wails_windows_amd64.syso` (a second manifest). FIX: empty the toolchain's `default-manifest.o` (`mingw64\x86_64-w64-mingw32\lib\default-manifest.o`) — back it up first (`.bak`). If the error persists, the Go link cache is stale: `go clean -cache` then rebuild. This is a machine-level workaround; reapplies only if MinGW is reinstalled/updated.
- Shell is Windows PowerShell 5.1: no `&&` in shell; chain with `;` or use `cmd; if ($?) { cmd }`.
- npm warns about `.npmrc` `minimum-release-age` (npm ignores it); harmless.

## Architecture

- `main.go` registers the Go service and embeds `frontend/dist`; app window is 1000x618.
- `pkg/services/cbz_service.go` — `CBZService` is the single exposed service (methods: `UnpackCBZ`, `CleanupTempFolder`, `ProcessCBZ`, `SaveCBZ`). Add new methods here; then regenerate bindings.
- `pkg/esrgan/esrgan.go` — Real-ESRGAN ONNX inference (`onnxruntime_go`): `FindLibrary`, `InitRuntime`/`DestroyRuntime` (env lifecycle), `New`/`Close` (session lifecycle), `Upscale`/`UpscaleOne` (tiled 4x, uint8). Uses the **DirectML execution provider** for GPU when available (automatic CPU fallback). **Tensors are created once per session and overwritten via `GetData()`** (no per-tile allocation). `UpscaleOne(img, workerIndex)` processes a single page serially with one session's isolated tensors, enabling **page-level parallelism** (each in-flight page uses a distinct session). `ProcessCBZ` runs pages in a worker pool limited to `Workers()` sessions, then writes the zip serially in order. `ProcessCBZ` emits a `cbz:progress` event with stage/message/percentage to the frontend.
- `pkg/real_esrgan_general_x4v3-onnx-w8a8/embed.go` (package `modeldata`) — embeds the **Real-ESRGAN General-x4-v3 (SRVGGNetCompact) x4 quantized w8a8** model (`real_esrgan_general_x4v3.onnx`, ~109 KB graph + ~4.7 MB external `.data` weights). w8a8 is much smaller/faster than float with no perceptible loss for manga/HDs. I/O names are `image`/`upscaled_image`; fixed 1×3×128×128 → 1×3×512×512, **uint8** (0-255) NCHW. Write BOTH `.onnx` and `.data` to the same temp dir before `onnxruntime` loads it (ORT resolves the external weights by basename).
- `pkg/yolo/yolo.go` — YOLO object detection (`yolo26n.onnx`, embedded in the same package, ONNX via onnxruntime): `New`/`Close` (session lifecycle, reuses the already-initialized ort env from `esrgan.InitRuntime`), `Detect(img)` (letterbox 640x640, returns `[]Box` in original coords, filtered by `ConfThreshold`), `BuildMask(w,h,margin,boxes)` (B&W gray where white = protected). Detection is **serialized with a mutex** (shared session/tensors). `ProcessCBZ` runs it per page to build a protection mask passed to `seamcarve.ResizeWithMask`.
- `pkg/yolo/embed.go` — embeds `yolo26n.onnx` (Ultralytics YOLOv8n, self-contained, ~9.5 MB). Input `images` [1,3,640,640]; output `output0` [1,300,6] = [x1,y1,x2,y2,score,class] in 640px, NMS baked in. opset 12.
- `frontend/bindings/...` — AUTO-GENERATED from Go service methods. Do not hand-edit; run `wails3 generate bindings -ts -i`. `App.tsx` imports the service via `../bindings/cbz-converter/pkg/services`.
- `pkg/cbz/` is currently empty scaffolding (converter.go/render.go).

## Data passing (Wails v3 binding quirk)

`[]byte` params and returns cross the boundary as **base64 strings** in TS. `ProcessCBZ` returns the processed CBZ as a base64 string; `SaveCBZ(path, fileData)` takes a base64 string. The frontend reads the uploaded file as base64 (`FileReader.readAsDataURL`, strip the `data:...;base64,` prefix).

## Backend processing

- Image decoding supports jpg/png/gif/webp/bmp/tiff via `golang.org/x/image`; pages are re-encoded as JPEG (quality 90) and renamed `001.jpg`, `002.jpg`, ... for correct reading order.
- `ProcessCBZ` reuses `UnpackCBZ` to extract to `os.TempDir()`, then cleans up via `CleanupTempFolder` (defer). Save-dialog and file writing live in the frontend (`Dialogs.SaveFile` from `@wailsio/runtime`) + `SaveCBZ`.
- Upscaling logic: when a page is smaller than the target resolution (`width > srcW || height > srcH`), run Real-ESRGAN 4x (`esr.Upscale`) then seam carving to exact dims; otherwise seam carve the source directly. `seamcarve.Resize` is pure Go (forward/backward energy, one seam at a time with energy recomputed each step). It applies a **center-weighted energy shield** (`SetCenterShield`) and a **levels contrast filter** (`SetLevels`) at the end. `ResizeWithMask(src, mask, w, h)` uses a **hybrid strategy**: connected protected components (from the mask, white = protected) are extracted and rescaled **uniformly (bicubic)**, while seam carving runs only on the background — this guarantees long diagonal protected objects (e.g. a sickle handle) are never sheared by seams alternating sides. `Resize` (no mask) falls back to pure carving. `fitToTarget` pre-scales to ~1.2x the target so seam carving only adjusts the residual. Upscale tiles use an effective 64x64 area centered in the 128x128 model input with 32px padding, discarding the padded output borders.

## UI conventions

- Themed by `frontend/public/style.css` (custom classes `input-box`, `input`, `btn`, `toast`, `badge`, `title`, `subtitle`). Reuse these rather than Tailwind utility classes to match the neon-dark look.
- Defaults in `App.tsx`: width `1080`, height `1920`.