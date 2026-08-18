package modeldata

import _ "embed"

// Modelo Real-ESRGAN general-x4-v3 (SRVGGNetCompact) x4, quantizado w8a8
// (weights/activations em int8), exportado pela Qualcomm. Muito menor e mais
// rápido que a versão float, sem perda visual perceptível em HQs/mangás.
// O grafo (.onnx) referencia os pesos em um arquivo externo (.data), por isso
// ambos precisam ser embutidos e gravados juntos no disco antes de onnxruntime
// carregar o modelo. Entrada/saída fixas em uint8 (0-255) NCHW:
// 1x3x128x128 -> 1x3x512x512.
//
//go:embed real_esrgan_general_x4v3.onnx
var onnx []byte

//go:embed real_esrgan_general_x4v3.data
var data []byte

// Onnx retorna os bytes do grafo do modelo.
func Onnx() []byte {
	return onnx
}

// Data retorna os bytes dos pesos externos do modelo.
func Data() []byte {
	return data
}
