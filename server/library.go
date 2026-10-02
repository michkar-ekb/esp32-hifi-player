package main

import (
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	_ "image/png"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/image/draw"
)

// Library is the music folder tree. Paths given to the web remote are relative to Root.
type Library struct {
	Root      string
	coverDir  string
	mu        sync.Mutex
	resizeMu  sync.Mutex
	durCache  map[string]float64 // path|size|mtime -> seconds
	cueCache  map[string]cueCacheEntry
	coverSeen map[string]coverCheck
}

func NewLibrary(root, dataDir string) *Library {
	cd := filepath.Join(dataDir, "covers")
	os.MkdirAll(cd, 0755)
	return &Library{Root: root, coverDir: cd, durCache: map[string]float64{}, coverSeen: map[string]coverCheck{}, cueCache: map[string]cueCacheEntry{}}
}

var errOutside = errors.New("path is outside the music folder")

// Abs turns a relative path from the remote into a real path inside Root.
func (l *Library) Abs(rel string) (string, error) {
	rel = filepath.Clean("/" + filepath.FromSlash(rel))
	full := filepath.Join(l.Root, rel)
	r, err := filepath.Rel(l.Root, full)
	if err != nil || strings.HasPrefix(r, "..") {
		return "", errOutside
	}
	return full, nil
}

func (l *Library) relOf(full string) string {
	r, _ := filepath.Rel(l.Root, full)
	if r == "." {
		return ""
	}
	return filepath.ToSlash(r)
}

type Entry struct {
	Name string  `json:"name"`
	Path string  `json:"path"`
	Dir  bool    `json:"dir"`
	Dur  float64 `json:"dur,omitempty"`
}

type Listing struct {
	Path   string  `json:"path"`
	Name   string  `json:"name"`
	Parent *string `json:"parent"`
	Cover  string  `json:"cover,omitempty"`
	Items  []Entry `json:"items"`
}

func (l *Library) Browse(rel string) (*Listing, error) {
	full, err := l.Abs(rel)
	if err != nil {
		return nil, err
	}
	des, err := os.ReadDir(full)
	if err != nil {
		return nil, err
	}
	out := &Listing{Path: l.relOf(full), Name: filepath.Base(full), Items: []Entry{}}
	if out.Path != "" {
		par := l.relOf(filepath.Dir(full))
		out.Parent = &par
	} else {
		out.Name = "Музыка"
	}
	hidden := map[string]bool{} // album files replaced by their CUE songs
	for _, de := range des {
		if !de.IsDir() && strings.EqualFold(filepath.Ext(de.Name()), ".cue") {
			cp := filepath.Join(full, de.Name())
			if sh := l.parseCue(cp); sh != nil {
				hidden[sh.Audio] = true
				for _, t := range sh.Tracks {
					e := Entry{Name: t.label(), Path: fmt.Sprintf("%s#%d", l.relOf(cp), t.Num)}
					if t.End > t.Start {
						e.Dur = t.End - t.Start
					}
					out.Items = append(out.Items, e)
				}
			}
		}
	}
	for _, de := range des {
		name := de.Name()
		if strings.HasPrefix(name, ".") || hidden[filepath.Join(full, name)] {
			continue
		}
		p := filepath.Join(full, name)
		st, err := os.Stat(p) // follows symlinks
		if err != nil {
			continue
		}
		if st.IsDir() {
			out.Items = append(out.Items, Entry{Name: name, Path: l.relOf(p), Dir: true})
		} else if isAudio(name) {
			out.Items = append(out.Items, Entry{Name: titleOf(name), Path: l.relOf(p), Dur: l.duration(p, st)})
		}
	}
	sort.SliceStable(out.Items, func(i, j int) bool {
		a, b := out.Items[i], out.Items[j]
		if a.Dir != b.Dir {
			return a.Dir
		}
		return naturalLess(strings.ToLower(a.Name), strings.ToLower(b.Name))
	})
	if l.findCover(full) != "" {
		out.Cover = "/api/cover?path=" + urlQuery(out.Path)
	}
	return out, nil
}

func (l *Library) duration(p string, st os.FileInfo) float64 {
	key := fmt.Sprintf("%s|%d|%d", p, st.Size(), st.ModTime().Unix())
	l.mu.Lock()
	d, ok := l.durCache[key]
	l.mu.Unlock()
	if !ok {
		d = fileDuration(p)
		l.mu.Lock()
		l.durCache[key] = d
		l.mu.Unlock()
	}
	return d
}

// Collect returns queue items for a file, or for every track inside a folder (recursively, in order).
func (l *Library) Collect(rel string) ([]Item, error) {
	if cue, num, ok := splitCuePath(rel); ok {
		return l.cueItems(cue, num)
	}
	if strings.EqualFold(filepath.Ext(rel), ".cue") {
		return l.cueItems(rel, 0)
	}
	full, err := l.Abs(rel)
	if err != nil {
		return nil, err
	}
	st, err := os.Stat(full)
	if err != nil {
		return nil, err
	}
	if !st.IsDir() {
		if !isAudio(full) {
			return nil, errors.New("not an audio file")
		}
		return []Item{{Kind: "file", Path: l.relOf(full), Title: titleOf(filepath.Base(full)), Dur: l.duration(full, st)}}, nil
	}
	var items []Item
	ls, err := l.Browse(rel)
	if err != nil {
		return nil, err
	}
	for _, e := range ls.Items {
		if e.Dir {
			sub, _ := l.Collect(e.Path)
			items = append(items, sub...)
		} else if _, _, ok := splitCuePath(e.Path); ok {
			sub, _ := l.Collect(e.Path)
			items = append(items, sub...)
		} else {
			items = append(items, Item{Kind: "file", Path: e.Path, Title: e.Name, Dur: e.Dur})
		}
	}
	return items, nil
}

func titleOf(name string) string { return strings.TrimSuffix(name, filepath.Ext(name)) }

// naturalLess sorts "2 x" before "10 x".
func naturalLess(a, b string) bool {
	for a != "" && b != "" {
		da, db := lead(a), lead(b)
		if da != "" && db != "" {
			if len(strings.TrimLeft(da, "0")) != len(strings.TrimLeft(db, "0")) {
				return len(strings.TrimLeft(da, "0")) < len(strings.TrimLeft(db, "0"))
			}
			if c := strings.Compare(strings.TrimLeft(da, "0"), strings.TrimLeft(db, "0")); c != 0 {
				return c < 0
			}
			a, b = a[len(da):], b[len(db):]
			continue
		}
		if a[0] != b[0] {
			return a[0] < b[0]
		}
		a, b = a[1:], b[1:]
	}
	return len(a) < len(b)
}

func lead(s string) string {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	return s[:i]
}

// ---------- covers ----------

var coverNames = []string{"cover", "folder", "front", "albumart", "album"}

type coverCheck struct {
	ok bool
	at time.Time
}

// hasCover answers from a one-minute cache: the state is polled every second.
func (l *Library) hasCover(dir string) bool {
	l.mu.Lock()
	c, ok := l.coverSeen[dir]
	l.mu.Unlock()
	if ok && time.Since(c.at) < time.Minute {
		return c.ok
	}
	has := l.findCover(dir) != ""
	l.mu.Lock()
	l.coverSeen[dir] = coverCheck{has, time.Now()}
	l.mu.Unlock()
	return has
}

func (l *Library) findCover(dir string) string {
	des, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	var any string
	for _, de := range des {
		ext := strings.ToLower(filepath.Ext(de.Name()))
		if de.IsDir() || (ext != ".jpg" && ext != ".jpeg" && ext != ".png") {
			continue
		}
		base := strings.ToLower(titleOf(de.Name()))
		for _, n := range coverNames {
			if base == n {
				return filepath.Join(dir, de.Name())
			}
		}
		if any == "" {
			any = filepath.Join(dir, de.Name())
		}
	}
	return any
}

// CoverFile returns a cached JPEG (max 600 px) of the folder's cover image.
func (l *Library) CoverFile(rel string) (string, error) {
	full, err := l.Abs(rel)
	if err != nil {
		return "", err
	}
	if st, err := os.Stat(full); err == nil && !st.IsDir() {
		full = filepath.Dir(full)
	}
	src := l.findCover(full)
	if src == "" {
		return "", os.ErrNotExist
	}
	st, err := os.Stat(src)
	if err != nil {
		return "", err
	}
	h := sha1.Sum([]byte(fmt.Sprintf("%s|%d|%d", src, st.Size(), st.ModTime().Unix())))
	dst := filepath.Join(l.coverDir, hex.EncodeToString(h[:])+".jpg")
	if _, err := os.Stat(dst); err == nil {
		return dst, nil
	}
	l.resizeMu.Lock() // one resize at a time: covers can be huge
	defer l.resizeMu.Unlock()
	f, err := os.Open(src)
	if err != nil {
		return "", err
	}
	img, _, err := image.Decode(f)
	f.Close()
	if err != nil {
		return "", err
	}
	b := img.Bounds()
	w, hgt := b.Dx(), b.Dy()
	const max = 600
	if w > max || hgt > max {
		if w >= hgt {
			w, hgt = max, hgt*max/w
		} else {
			w, hgt = w*max/hgt, max
		}
	}
	out := image.NewRGBA(image.Rect(0, 0, w, hgt))
	draw.CatmullRom.Scale(out, out.Bounds(), img, b, draw.Src, nil)
	tmp := dst + ".tmp"
	of, err := os.Create(tmp)
	if err != nil {
		return "", err
	}
	err = jpeg.Encode(of, out, &jpeg.Options{Quality: 85})
	of.Close()
	if err != nil {
		return "", err
	}
	return dst, os.Rename(tmp, dst)
}

func urlQuery(s string) string { return url.QueryEscape(filepath.ToSlash(s)) }

func fmtLabel(it Item, f Format) string {
	kind := "Радио"
	if it.Kind == "file" {
		kind = strings.ToUpper(strings.TrimPrefix(filepath.Ext(it.Path), "."))
	}
	khz := strings.Replace(strings.TrimSuffix(strings.TrimSuffix(fmt.Sprintf("%.1f", float64(f.Rate)/1000), "0"), "."), ".", ",", 1)
	s := fmt.Sprintf("%s · %s кГц · %d бит", kind, khz, f.Bits)
	if f.Codec != "" {
		s = fmt.Sprintf("%s → стерео · %s кГц · %d бит", strings.ToUpper(f.Codec), khz, f.Bits)
	}
	if kind == "DSF" || kind == "DFF" {
		s = "DSD → PCM " + khz + " кГц · 24 бит"
	}
	return s
}
