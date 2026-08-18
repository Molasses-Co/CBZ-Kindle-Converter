# setup-onnxruntime.ps1
# Garante a onnxruntime.dll com suporte a DirectML (aceleração por GPU) ao lado
# do executável em bin\. O backend espera a DLL neste local (ou via
# ONNXRUNTIME_SHARED_LIBRARY_PATH).
#
# Uso:
#   powershell -ExecutionPolicy Bypass -File scripts\setup-onnxruntime.ps1
#
# Origem: pacote NuGet Microsoft.ML.OnnxRuntime.DirectML (v1.24.4), o build do
# onnxruntime com o execution provider DirectML. A versão 1.24.x é a mais nova
# disponível com DirectML e é compatível com o wrapper onnxruntime_go v1.24.0
# (API 24). O build já embute o DirectML (sem DirectML.dll separada).
#
# DLLs copiadas para bin\:
#   - onnxruntime.dll
#   - onnxruntime_providers_shared.dll (dependência de providers)

$ErrorActionPreference = 'Stop'

$root = Split-Path -Parent $PSScriptRoot
$destDir = Join-Path $root 'bin'
New-Item -ItemType Directory -Force -Path $destDir | Out-Null

$version = '1.24.4'
$nupkg = Join-Path $env:TEMP "Microsoft.ML.OnnxRuntime.DirectML.$version.nupkg"
$url = "https://api.nuget.org/v3-flatcontainer/microsoft.ml.onnxruntime.directml/$version/microsoft.ml.onnxruntime.directml.$version.nupkg"
$extract = Join-Path $env:TEMP "onnxruntime-directml-$version"

if (-not (Test-Path (Join-Path $destDir 'onnxruntime.dll')) -or
    -not (Test-Path (Join-Path $destDir 'onnxruntime_providers_shared.dll'))) {
    Write-Host "Baixando onnxruntime DirectML $version de $url ..."
    Invoke-WebRequest -Uri $url -OutFile $nupkg
    if (Test-Path $extract) { Remove-Item $extract -Recurse -Force }
    # o nuget é um .zip; copie para .zip para o Expand-Archive aceitar
    $zip = [IO.Path]::ChangeExtension($nupkg, '.zip')
    Copy-Item $nupkg $zip -Force
    Expand-Archive -Path $zip -DestinationPath $extract -Force
}

$native = Join-Path $extract "runtimes\win-x64\native"

$dll = Join-Path $native 'onnxruntime.dll'
$shared = Join-Path $native 'onnxruntime_providers_shared.dll'
if (-not (Test-Path $dll)) {
    throw "onnxruntime.dll nao encontrada no pacote baixado."
}

Copy-Item $dll (Join-Path $destDir 'onnxruntime.dll') -Force
if (Test-Path $shared) {
    Copy-Item $shared (Join-Path $destDir 'onnxruntime_providers_shared.dll') -Force
}
Write-Host "Pronto. onnxruntime.dll (DirectML) copiada para $destDir"