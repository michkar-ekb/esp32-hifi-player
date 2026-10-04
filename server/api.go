package main

import (
	"embed"
	"encoding/json"
	"errors"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
)

//go:embed web
var webFS embed.FS

// ---------- radio stations ----------

type Station struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	URL  string `json:"url"`
}

type Radios struct {
	mu   sync.Mutex
	path string
	List []Station
}

func NewRadios(dataDir string) *Radios {
	r := &Radios{path: filepath.Join(dataDir, "radios.json")}
	if b, err := os.ReadFile(r.path); err == nil {
		json.Unmarshal(b, &r.List)
		// ids must survive a trip through JavaScript numbers (exact only up to 2^53):
		// early versions used nanosecond timestamps, renumber those
		changed := false
		for i := range r.List {
			if r.List[i].ID <= 0 || r.List[i].ID >= 1<<53 {
				r.List[i].ID = r.nextID()
				changed = true
			}
		}
		if changed {
			r.save()
		}
	} else {
		r.List = []Station{
			{1, "Radio Paradise", "http://stream.radioparadise.com/mp3-192"},
			{2, "SomaFM Groove Salad", "http://ice1.somafm.com/groovesalad-128-mp3"},
			{3, "FIP", "http://icecast.radiofrance.fr/fip-midfi.mp3"},
		}
		r.save()
	}
	return r
}

func (r *Radios) save() {
	b, _ := json.MarshalIndent(r.List, "", " ")
	os.WriteFile(r.path, b, 0644)
}

// nextID is one more than the biggest small id in use.
func (r *Radios) nextID() int64 {
	var m int64
	for _, s := range r.List {
		if s.ID > m && s.ID < 1<<53 {
			m = s.ID
		}
	}
	return m + 1
}

func (r *Radios) Get(id int64) (Station, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, s := range r.List {
		if s.ID == id {
			return s, true
		}
	}
	return Station{}, false
}

// ---------- HTTP ----------

type Server struct {
	lib    *Library
	player *Player
	radios *Radios
}

func (s *Server) Routes() http.Handler {
	m := http.NewServeMux()
	web, _ := fs.Sub(webFS, "web")
	m.Handle("GET /", http.FileServer(http.FS(web)))

	m.HandleFunc("GET /api/browse", func(w http.ResponseWriter, r *http.Request) {
		ls, err := s.lib.Browse(r.URL.Query().Get("path"))
		reply(w, ls, err)
	})
	m.HandleFunc("GET /api/state", func(w http.ResponseWriter, r *http.Request) {
		reply(w, s.player.State(), nil)
	})
	m.HandleFunc("GET /api/cover", func(w http.ResponseWriter, r *http.Request) {
		f, err := s.lib.CoverFile(r.URL.Query().Get("path"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "max-age=86400")
		http.ServeFile(w, r, f)
	})
	// {"path": "..."} or {"radio": id}; "now": true = play now, false = add to the queue.
	// Playing a folder or an album replaces the queue; a single song or a station plays on its own.
	m.HandleFunc("POST /api/queue", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Path  *string `json:"path"`
			Radio int64   `json:"radio"`
			Now   bool    `json:"now"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			reply(w, nil, err)
			return
		}
		var items []Item
		var err error
		if req.Radio != 0 {
			st, ok := s.radios.Get(req.Radio)
			if !ok {
				reply(w, nil, errors.New("no such station"))
				return
			}
			items = []Item{{Kind: "radio", URL: st.URL, Title: st.Name}}
		} else if req.Path != nil {
			items, err = s.lib.Collect(*req.Path)
		}
		if err == nil && len(items) == 0 {
			err = errors.New("здесь нет музыки")
		}
		if err != nil {
			reply(w, nil, err)
			return
		}
		if req.Now {
			if len(items) == 1 && (req.Radio != 0 || s.lib.isSong(*req.Path)) {
				s.player.PlaySolo(items[0])
			} else {
				s.player.PlayFolder(items)
			}
			reply(w, map[string]int{"added": len(items)}, nil)
		} else {
			n := s.player.Add(items)
			reply(w, map[string]int{"added": len(items), "length": n}, nil)
		}
	})
	m.HandleFunc("POST /api/cmd", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Cmd string  `json:"cmd"`
			Arg float64 `json:"arg"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			reply(w, nil, err)
			return
		}
		err := s.player.Command(req.Cmd, req.Arg)
		reply(w, s.player.State(), err)
	})
	m.HandleFunc("GET /api/radios", func(w http.ResponseWriter, r *http.Request) {
		s.radios.mu.Lock()
		defer s.radios.mu.Unlock()
		reply(w, s.radios.List, nil)
	})
	m.HandleFunc("POST /api/radios", func(w http.ResponseWriter, r *http.Request) {
		var st Station
		if err := json.NewDecoder(r.Body).Decode(&st); err != nil || st.Name == "" || st.URL == "" {
			reply(w, nil, errors.New("нужны название и адрес потока"))
			return
		}
		s.radios.mu.Lock()
		st.ID = s.radios.nextID()
		s.radios.List = append(s.radios.List, st)
		s.radios.save()
		s.radios.mu.Unlock()
		reply(w, st, nil)
	})
	m.HandleFunc("DELETE /api/radios", func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
		s.radios.mu.Lock()
		for i, st := range s.radios.List {
			if st.ID == id {
				s.radios.List = append(s.radios.List[:i], s.radios.List[i+1:]...)
				break
			}
		}
		s.radios.save()
		s.radios.mu.Unlock()
		reply(w, map[string]bool{"ok": true}, nil)
	})

	// ---------- ESP32 player ----------
	// GET /esp/poll?epoch=E&played=N&buf=ms&rssi=dBm&kbps=K[&resync=1] -> "epoch state volume\n"
	m.HandleFunc("GET /esp/poll", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		e, _ := strconv.ParseUint(q.Get("epoch"), 10, 32)
		played, _ := strconv.ParseUint(q.Get("played"), 10, 64)
		buf, _ := strconv.Atoi(q.Get("buf"))
		rssi, _ := strconv.Atoi(q.Get("rssi"))
		kbps, _ := strconv.Atoi(q.Get("kbps"))
		epoch, state, vol := s.player.Poll(uint32(e), played, buf, rssi, kbps, q.Get("resync") == "1")
		// the player's own settings page, linked from the remote; only reachable when it is in the same network
		if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
			if ip := net.ParseIP(host); ip != nil && ip.IsPrivate() {
				s.player.SetSetupURL("http://" + host + "/")
			} else {
				s.player.SetSetupURL("")
			}
		}
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte(strconv.FormatUint(uint64(epoch), 10) + " " + state + " " + strconv.Itoa(vol) + "\n"))
	})
	// GET /esp/stream?epoch=E&lane=L&lanes=N (HTTP/1.0) -> PCM chunks of that epoch for lane L
	m.HandleFunc("GET /esp/stream", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		e, _ := strconv.ParseUint(q.Get("epoch"), 10, 32)
		lane, _ := strconv.Atoi(q.Get("lane"))
		lanes, err := strconv.Atoi(q.Get("lanes"))
		if err != nil {
			lanes = 1
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(200)
		fl, _ := w.(http.Flusher)
		flush := func() {
			if fl != nil {
				fl.Flush()
			}
		}
		flush()
		s.player.Lane(uint32(e), lane, lanes, w, flush, r.Context().Done())
	})
	return m
}

func reply(w http.ResponseWriter, v any, err error) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if err != nil {
		w.WriteHeader(400)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	json.NewEncoder(w).Encode(v)
}
