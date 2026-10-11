// S3 Hi-Fi streaming server: keeps the music library and the queue, decodes FLAC / MP3 /
// WAV / DSD and MP3 internet radio to PCM and streams it to the ESP32-S3 player.
// The web remote for the phone is built in.
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// Version is set at build time: go build -ldflags "-X main.Version=0.4.0"
var Version = "dev"

func main() {
	home, _ := os.UserHomeDir()
	music := flag.String("music", filepath.Join(home, "Music"), "music folder")
	data := flag.String("data", filepath.Join(home, ".s3hifi"), "folder for settings, queue and cover cache")
	listen := flag.String("listen", ":8097", "address of the web remote and the player stream")
	sacdInfo := flag.String("sacd-info", "", "print the track list of a SACD .iso and exit (diagnostics)")
	showVersion := flag.Bool("version", false, "print the version and exit")
	onlineArt := flag.Bool("online-covers", true, "look up missing album covers and radio song pictures on the internet (iTunes)")
	tray := flag.Bool("tray", false, "Windows: run without a console, with an icon in the notification area (log goes to <data>/s3hifi.log)")
	flag.Parse()
	if *showVersion {
		fmt.Println("S3 Hi-Fi server", Version)
		return
	}
	if *sacdInfo != "" {
		d, err := parseSACD(*sacdInfo)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("DST: %v, channels: %d, area sectors %d..%d\n", d.DST, d.Channels, d.AreaLSN[0], d.AreaLSN[1])
		for _, t := range d.Tracks {
			fmt.Printf("%2d  %-40s %-20s lsn %d +%d  %.1f s\n", t.Num, t.Title, t.Performer, t.StartLSN, t.LenLSN, t.seconds())
		}
		return
	}

	os.MkdirAll(*data, 0755)
	if *tray {
		logToFile(filepath.Join(*data, "s3hifi.log"))
	}
	// the music folder: -music if given, else the one chosen earlier from the tray menu, else ~/Music
	musicSet := false
	flag.Visit(func(f *flag.Flag) { musicSet = musicSet || f.Name == "music" })
	chosen := ""
	if !musicSet {
		chosen = savedMusicDir(*data)
		if chosen != "" {
			*music = chosen
		}
	}
	root, err := filepath.Abs(*music)
	if err != nil {
		log.Fatal(err)
	}
	if st, err := os.Stat(root); err != nil || !st.IsDir() {
		// not fatal: the folder may be a network share that comes up later; radio works meanwhile
		log.Printf("music folder %q is not available yet: %v", root, err)
	}

	lib := NewLibrary(root, *data)
	lib.art = NewArtwork(*data, *onlineArt)
	s := &Server{lib: lib, player: NewPlayer(lib, *data), radios: NewRadios(*data)}
	go answerDiscovery(*listen)
	log.Printf("S3 Hi-Fi server %s: music %s, remote http://<this-pc>%s", Version, root, *listen)
	if *tray {
		// no folder chosen yet and none given: the tray asks for one right away
		runTray(*listen, lib, *data, s.Routes(), !musicSet && chosen == "")
		return
	}
	log.Fatal(http.ListenAndServe(*listen, s.Routes()))
}

// savedMusicDir is the music folder chosen from the tray menu (<data>/music-folder.txt), or "".
func savedMusicDir(data string) string {
	b, err := os.ReadFile(filepath.Join(data, "music-folder.txt"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func saveMusicDir(data, dir string) error {
	return os.WriteFile(filepath.Join(data, "music-folder.txt"), []byte(dir+"\n"), 0644)
}

// logToFile sends the log to a file (kept small: started anew when it grows past 5 MB).
func logToFile(path string) {
	if st, err := os.Stat(path); err == nil && st.Size() > 5<<20 {
		os.Rename(path, path+".old")
	}
	if f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644); err == nil {
		log.SetOutput(f)
	}
}
