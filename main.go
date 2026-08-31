package main

import (
	"embed"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
)

//go:embed all:gui
var guiFS embed.FS

func main() {
	app := NewApp()

	err := wails.Run(&options.App{
		Title:     "QuickDrop",
		Width:     960,
		Height:    640,
		MinWidth:  680,
		MinHeight: 560,
		AssetServer: &assetserver.Options{
			Assets: guiFS,
		},
		OnStartup:   app.startup,
		OnDomReady:  app.domReady,
		OnShutdown:  app.shutdown,
		Bind:        []interface{}{app},
	})
	if err != nil {
		println("Error:", err.Error())
	}
}