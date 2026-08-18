package yolo

import _ "embed"

// Modelo YOLOv8n Ultralytics (yolo26n) exportado para ONNX (opset 12).
// Entrada 1x3x640x640 (BCHW, float32); saída output0 [1,300,6] =
// [x1, y1, x2, y2, score, class] em pixels de 640, NMS já aplicado. Autocontido.
//
//go:embed yolo26n.onnx
var yolo26nOnnx []byte

func modeldataOnnx() []byte {
	return yolo26nOnnx
}
