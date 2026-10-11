//go:build windows

package main

import (
	_ "embed"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"unsafe"

	"fyne.io/systray"
)

//go:embed winres/s3hifi.ico
var trayIcon []byte

// runTray runs the server with an icon in the notification area: open the remote,
// open the music folder, open the log, exit. A second copy is not started: if the port
// is taken, the user gets a message and the remote of the running copy opens.
func runTray(listen, music, data string, h http.Handler) {
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		log.Printf("cannot listen on %s: %v", listen, err)
		messageBox("S3 Hi-Fi", "Сервер уже запущен или порт "+listen+" занят.\n\n"+err.Error())
		openURL(remoteURL(listen))
		os.Exit(1)
	}
	go func() {
		log.Printf("server stopped: %v", http.Serve(ln, h))
	}()

	systray.Run(func() {
		systray.SetIcon(trayIcon)
		systray.SetTitle("S3 Hi-Fi")
		systray.SetTooltip("S3 Hi-Fi сервер " + Version + "\nМузыка: " + music)

		mRemote := systray.AddMenuItem("Открыть пульт", "Пульт в браузере")
		mMusic := systray.AddMenuItem("Папка с музыкой", music)
		mLog := systray.AddMenuItem("Журнал", "Файл журнала сервера")
		systray.AddSeparator()
		mVer := systray.AddMenuItem("Версия "+Version, "")
		mVer.Disable()
		mQuit := systray.AddMenuItem("Выход", "Остановить сервер")

		systray.SetOnTapped(func() { openURL(remoteURL(listen)) })
		go func() {
			for {
				select {
				case <-mRemote.ClickedCh:
					openURL(remoteURL(listen))
				case <-mMusic.ClickedCh:
					exec.Command("explorer", music).Start()
				case <-mLog.ClickedCh:
					exec.Command("notepad", filepath.Join(data, "s3hifi.log")).Start()
				case <-mQuit.ClickedCh:
					systray.Quit()
				}
			}
		}()
	}, func() {
		log.Printf("exit from the tray menu")
		os.Exit(0)
	})
}

// remoteURL turns ":8097" or "0.0.0.0:8097" into an address the local browser can open.
func remoteURL(listen string) string {
	_, port, err := net.SplitHostPort(listen)
	if err != nil {
		port = "8097"
	}
	return "http://localhost:" + port + "/"
}

func openURL(u string) {
	exec.Command("rundll32", "url.dll,FileProtocolHandler", u).Start()
}

func messageBox(title, text string) {
	t, _ := syscall.UTF16PtrFromString(title)
	m, _ := syscall.UTF16PtrFromString(text)
	syscall.NewLazyDLL("user32.dll").NewProc("MessageBoxW").Call(0,
		uintptr(unsafe.Pointer(m)), uintptr(unsafe.Pointer(t)), 0x40) // MB_ICONINFORMATION
}
