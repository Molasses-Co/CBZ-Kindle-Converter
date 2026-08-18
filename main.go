package main

import (
	"cbz-converter/pkg/services"
	"embed"
	"log"

	"github.com/wailsapp/wails/v3/pkg/application"
)

//go:embed all:frontend/dist
var assets embed.FS

// init registra o evento de progresso do processamento, permitindo que o
// gerador de bindings publique a tipagem correspondente ao frontend.
func init() {
	application.RegisterEvent[services.CBZProgress]("cbz:progress")
}

func main() {
	app := application.New(application.Options{
		Name:        "cbz-converter",
		Description: "Conversor e Leitor de CBZ",
		Services: []application.Service{
			application.NewService(&services.CBZService{}), // Serviço registrado
		},
		Assets: application.AssetOptions{
			Handler: application.AssetFileServerFS(assets),
		},
	})

	app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:  "CBZ Converter",
		Width:  1000,
		Height: 618,
		URL:    "/",
	})

	err := app.Run()
	if err != nil {
		log.Fatal(err)
	}
}
