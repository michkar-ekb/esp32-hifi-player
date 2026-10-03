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
)

func main() {
	home, _ := os.UserHomeDir()
	music := flag.String("music", filepath.Join(home, "Music"), "music folder")
	data := flag.String("data", filepath.Join(home, ".s3hifi"), "folder for settings, queue and cover cache")
	listen := flag.String("listen", ":8097", "address of the web remote and the player stream")
	sacdInfo := flag.String("sacd-info", "", "print the track list of a SACD .iso and exit (diagnostics)")
	flag.Parse()
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

	root, err := filepath.Abs(*music)
	if err != nil {
		log.Fatal(err)
	}
	if st, err := os.Stat(root); err != nil || !st.IsDir() {
		// not fatal: the folder may be a network share that comes up later; radio works meanwhile
		log.Printf("music folder %q is not available yet: %v", root, err)
	}
	os.MkdirAll(*data, 0755)

	lib := NewLibrary(root, *data)
	s := &Server{lib: lib, player: NewPlayer(lib, *data), radios: NewRadios(*data)}
	go answerDiscovery(*listen)
	log.Printf("S3 Hi-Fi server: music %s, remote http://<this-pc>%s", root, *listen)
	log.Fatal(http.ListenAndServe(*listen, s.Routes()))
}
