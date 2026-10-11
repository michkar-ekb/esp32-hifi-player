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
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"unsafe"

	"fyne.io/systray"
)

//go:embed winres/s3hifi.ico
var trayIcon []byte

var (
	user32   = syscall.NewLazyDLL("user32.dll")
	shell32  = syscall.NewLazyDLL("shell32.dll")
	ole32    = syscall.NewLazyDLL("ole32.dll")
	mpr      = syscall.NewLazyDLL("mpr.dll")
	choosing atomic.Bool // a folder dialog is open
)

// runTray runs the server with an icon in the notification area. Left or right click opens the menu:
// open the remote, the music folder, choose another music folder, the log, exit. A second copy is
// not started: if the port is taken, the user gets a message and the remote of the running copy opens.
// askFolder: no music folder has been chosen yet, so the folder dialog opens right away.
func runTray(listen string, lib *Library, data string, h http.Handler, askFolder bool) {
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

		mRemote := systray.AddMenuItem("Открыть пульт", "Пульт в браузере")
		mMusic := systray.AddMenuItem("", "Открыть папку с музыкой")
		mChoose := systray.AddMenuItem("Выбрать папку с музыкой…", `Любая папка: на этом компьютере, сетевой диск или \\компьютер\папка`)
		mLog := systray.AddMenuItem("Журнал", "Файл журнала сервера")
		systray.AddSeparator()
		mVer := systray.AddMenuItem("Версия "+Version, "")
		mVer.Disable()
		mQuit := systray.AddMenuItem("Выход", "Остановить сервер")

		show := func() {
			dir := lib.root()
			mMusic.SetTitle("Музыка: " + shortPath(dir))
			systray.SetTooltip("S3 Hi-Fi сервер " + Version + "\nМузыка: " + dir)
		}
		choose := func(first bool) {
			if !choosing.CompareAndSwap(false, true) {
				return
			}
			defer choosing.Store(false)
			title := "Выберите папку с музыкой"
			if first {
				title = "S3 Hi-Fi: выберите папку с музыкой. Сетевой диск тоже подойдёт; путь можно вписать в строку ниже."
			}
			dir, ok := browseFolder(title, lib.root())
			if !ok {
				return
			}
			dir = networkPath(dir)
			lib.SetRoot(dir)
			if err := saveMusicDir(data, dir); err != nil {
				log.Printf("cannot save the music folder: %v", err)
			}
			log.Printf("music folder: %s", dir)
			show()
		}
		show()
		if askFolder {
			go choose(true)
		}

		go func() {
			for {
				select {
				case <-mRemote.ClickedCh:
					openURL(remoteURL(listen))
				case <-mMusic.ClickedCh:
					exec.Command("explorer", lib.root()).Start()
				case <-mChoose.ClickedCh:
					go choose(false)
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

// shortPath keeps a long folder name readable in the menu.
func shortPath(p string) string {
	r := []rune(p)
	if len(r) <= 50 {
		return p
	}
	return string(r[:20]) + "…" + string(r[len(r)-28:])
}

// networkPath turns a folder on a mapped network drive (M:\Music) into its network path
// (\\server\share\Music): at logon the server may start before Windows reconnects the drive letter.
func networkPath(dir string) string {
	if len(dir) < 2 || dir[1] != ':' {
		return dir
	}
	local, _ := syscall.UTF16PtrFromString(dir[:2])
	buf := make([]uint16, 1024)
	n := uint32(len(buf))
	r, _, _ := mpr.NewProc("WNetGetConnectionW").Call(uintptr(unsafe.Pointer(local)),
		uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&n)))
	if r != 0 { // a local drive (or the mapping is unknown): keep the letter
		return dir
	}
	remote := strings.TrimRight(syscall.UTF16ToString(buf), `\`)
	return remote + dir[2:]
}

type browseInfo struct {
	owner       uintptr
	root        uintptr
	displayName *uint16
	title       *uint16
	flags       uint32
	callback    uintptr
	param       uintptr
	image       int32
}

const (
	bifReturnOnlyFSDirs = 0x0001
	bifEditBox          = 0x0010
	bifNewDialogStyle   = 0x0040
	bifShareable        = 0x8000
	bffmInitialized     = 1
	bffmSetSelectionW   = 0x0467
)

var browseStart *uint16 // folder selected when the dialog opens

var browseCallback = syscall.NewCallback(func(hwnd, msg, lp, data uintptr) uintptr {
	if msg == bffmInitialized && browseStart != nil {
		user32.NewProc("SendMessageW").Call(hwnd, bffmSetSelectionW, 1, uintptr(unsafe.Pointer(browseStart)))
	}
	return 0
})

// browseFolder shows the Windows folder dialog (it sees mapped network drives and \\server\share,
// and has a line to type a path). ok is false when the user cancels.
func browseFolder(title, start string) (string, bool) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	ole32.NewProc("CoInitializeEx").Call(0, 2) // COINIT_APARTMENTTHREADED
	defer ole32.NewProc("CoUninitialize").Call()

	name := make([]uint16, 260)
	t, _ := syscall.UTF16PtrFromString(title)
	browseStart, _ = syscall.UTF16PtrFromString(start)
	bi := browseInfo{
		displayName: &name[0],
		title:       t,
		flags:       bifReturnOnlyFSDirs | bifEditBox | bifNewDialogStyle | bifShareable,
		callback:    browseCallback,
	}
	pidl, _, _ := shell32.NewProc("SHBrowseForFolderW").Call(uintptr(unsafe.Pointer(&bi)))
	if pidl == 0 {
		return "", false
	}
	defer ole32.NewProc("CoTaskMemFree").Call(pidl)
	path := make([]uint16, 32768)
	r, _, _ := shell32.NewProc("SHGetPathFromIDListEx").Call(pidl, uintptr(unsafe.Pointer(&path[0])), uintptr(len(path)), 0)
	if r == 0 {
		return "", false
	}
	return syscall.UTF16ToString(path), true
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
	user32.NewProc("MessageBoxW").Call(0,
		uintptr(unsafe.Pointer(m)), uintptr(unsafe.Pointer(t)), 0x40) // MB_ICONINFORMATION
}
