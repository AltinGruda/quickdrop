// Command quickdrop-cli runs the same QuickDrop server without the desktop
// window, opening the browser control page instead. It is a developer/testing
// entry point; end users get the Wails GUI.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"time"

	"quickdrop/server"
)

func main() {
	dest := flag.String("dest", server.DefaultDestDir(), "folder where files land")
	flag.Parse()

	srv, err := server.Start(server.Config{DestDir: *dest})
	if err != nil {
		fmt.Fprintln(os.Stderr, "QuickDrop:", err)
		os.Exit(1)
	}

	fmt.Println("Open QuickDrop on your phone:")
	fmt.Println("  " + srv.URL())
	fmt.Println()
	fmt.Println("Control page (open in a browser on this computer):")
	fmt.Println("  " + srv.ControlURL())
	fmt.Println()
	fmt.Println("Files land in: " + srv.DestDir())

	qr, err := srv.QRPNG(512)
	if err == nil {
		_ = os.WriteFile("qr.png", qr, 0o644)
		fmt.Println("QR saved to qr.png")
	}

	time.Sleep(300 * time.Millisecond)
	if err := openBrowser(srv.ControlURL()); err != nil {
		fmt.Fprintln(os.Stderr, "open browser:", err)
	}

	select {}
}

func openBrowser(url string) error {
	switch runtime.GOOS {
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	case "darwin":
		return exec.Command("open", url).Start()
	default:
		return exec.Command("xdg-open", url).Start()
	}
}