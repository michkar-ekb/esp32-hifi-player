// S3 Hi-Fi streaming server: keeps the music library and the queue, decodes FLAC / MP3 /
// WAV / DSD and MP3 internet radio to PCM and streams it to the ESP32-S3 player.
// The web remote for the phone is built in.
package main

import (
	"flag"
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
	flag.Parse()

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
	log.Printf("S3 Hi-Fi server: music %s, remote http://<this-pc>%s", root, *listen)
	log.Fatal(http.ListenAndServe(*listen, s.Routes()))
}
