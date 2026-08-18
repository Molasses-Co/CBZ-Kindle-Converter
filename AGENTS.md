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
- `onnxruntime.dll` (DirectML build v1.24.4) + `onnxruntime_providers_shared.dll` are now **embedded in the exe** via `pkg/ortdll` (`go:embed`) and extracted to a temp dir at first init — the exe is self-contained (no DLLs needed next to it). `esrgan.FindLibrary` still searches `ONNXRUNTIME_SHARED_LIBRARY_PATH`, the exe dir and cwd first, and only then falls back to extraction. The source DLLs live in `pkg/ortdll/` (committed normally, ~17 MB, NOT LFS — `go:embed` needs real bytes). Reproduce them with `scripts/setup-onnxruntime.ps1` (downloads `Microsoft.ML.OnnxRuntime.DirectML` from NuGet) and copy into `pkg/ortdll/`. This DirectML build requires `onnxruntime_go` **v1.24.0** (API 24) — do NOT bump to a newer version without also bumping the DLL, or the API-version mismatch breaks init.
- **cgo + Wails manifest conflict**: `wails3 build CGO_ENABLED=1` fails the link with `ld.exe: .rsrc merge failure: multiple non-default manifests`. Cause: the gcc/MinGW driver auto-links its `default-manifest.o` (one manifest) which collides with the app manifest embedded in the `wails_windows_amd64.syso` (a second manifest). FIX: empty the toolchain's `default-manifest.o` (`mingw64\x86_64-w64-mingw32\lib\default-manifest.o`) — back it up first (`.bak`). If the error persists, the Go link cache is stale: `go clean -cache` then rebuild. This is a machine-level workaround; reapplies only if MinGW is reinstalled/updated.
- Shell is Windows PowerShell 5.1: no `&&` in shell; chain with `;` or use `cmd; if ($?) { cmd }`.
- npm warns about `.npmrc` `minimum-release-age` (npm ignores it); harmless.

## Architecture

- `main.go` registers the Go service and embeds `frontend/dist`; app window is 1000x618.
- `pkg/services/cbz_service.go` — `CBZService` is the single exposed service (methods: `UnpackCBZ`, `CleanupTempFolder`, `ProcessCBZ`, `SaveCBZ`). Add new methods here; then regenerate bindings.
- `pkg/esrgan/esrgan.go` — Real-ESRGAN ONNX inference (`onnxruntime_go`): `FindLibrary`, `InitRuntime`/`DestroyRuntime` (env lifecycle), `New`/`Close` (session lifecycle), `Upscale`/`UpscaleOne` (tiled 4x, uint8). Uses the **DirectML execution provider** for GPU when available (automatic CPU fallback). **Tensors are created once per session and overwritten via `GetData()`** (no per-tile allocation). `UpscaleOne(img, workerIndex)` processes a single page serially with one session's isolated tensors, enabling **page-level parallelism** (each in-flight page uses a distinct session). `ProcessCBZ` runs pages in a worker pool limited to `Workers()` sessions, then writes the zip serially in order. `ProcessCBZ` emits a `cbz:progress` event with stage/message/percentage to the frontend.
- `pkg/real_esrgan_general_x4v3-onnx-w8a8/embed.go` (package `modeldata`) — embeds the **Real-ESRGAN General-x4-v3 (SRVGGNetCompact) x4 quantized w8a8** model (`real_esrgan_general_x4v3.onnx`, ~109 KB graph + ~4.7 MB external `.data` weights). w8a8 is much smaller/faster than float with no perceptible loss for manga/HDs. I/O names are `image`/`upscaled_image`; fixed 1×3×128×128 → 1×3×512×512, **uint8** (0-255) NCHW. Write BOTH `.onnx` and `.data` to the same temp dir before `onnxruntime` loads it (ORT resolves the external weights by basename).
- `pkg/ortdll/embed.go` — embeds `onnxruntime.dll` (DirectML) + `onnxruntime_providers_shared.dll` via `go:embed`; `Extract()` writes both to a temp dir (idempotent, mutex-guarded) and returns the DLL path. `esrgan.FindLibrary` uses it as a last-resort fallback so the exe is self-contained.
- `pkg/retarget/retarget.go` — target-resolution fitting (replaces the removed seamcarve/crop): treats the target (`dstW x dstH`) as a **maximum box** and scales uniformly to fit inside it, preserving the page aspect. Never crops, stretches, or adds bars (`Fit(src, dstW, dstH)`). When the page aspect equals the target aspect, the result is exactly `dstW x dstH`.
- `frontend/bindings/...` — AUTO-GENERATED from Go service methods. Do not hand-edit; run `wails3 generate bindings -ts -i`. `App.tsx` imports the service via `../bindings/cbz-converter/pkg/services`.

## Data passing (Wails v3 binding quirk)

`[]byte` params and returns cross the boundary as **base64 strings** in TS. `ProcessCBZ` returns the processed CBZ as a base64 string; `SaveCBZ(path, fileData)` takes a base64 string. The frontend reads the uploaded file as base64 (`FileReader.readAsDataURL`, strip the `data:...;base64,` prefix).

## Backend processing

- Image decoding supports jpg/png/gif/webp/bmp/tiff via `golang.org/x/image`; pages are re-encoded as JPEG (quality 90) and renamed `001.jpg`, `002.jpg`, ... for correct reading order.
- `ProcessCBZ` reuses `UnpackCBZ` to extract to `os.TempDir()`, then cleans up via `CleanupTempFolder` (defer). Save-dialog and file writing live in the frontend (`Dialogs.SaveFile` from `@wailsio/runtime`) + `SaveCBZ`.
- Upscaling logic: when a page is smaller than the target resolution (`width > srcW || height > srcH`), run Real-ESRGAN 4x (`esr.Upscale`) then `retarget.Fit`; otherwise `retarget.Fit` the source directly. `retarget.Fit(src, w, h)` scales uniformly to fit inside the target box (never crops). Upscale tiles use an effective 64x64 area centered in the 128x128 model input with 32px padding, discarding the padded output borders.

## UI conventions

- Themed by `frontend/public/style.css` (custom classes `input-box`, `input`, `btn`, `toast`, `badge`, `title`, `subtitle`). Reuse these rather than Tailwind utility classes to match the neon-dark look.
- Defaults in `App.tsx`: width `1080`, height `1920`.