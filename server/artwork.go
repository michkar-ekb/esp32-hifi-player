package main

// Pictures from the internet when there is none of our own: the album cover for a folder
// without cover.jpg, and the song's cover for internet radio ("Artist - Title" from the stream).
// Looked up in the iTunes Search API, kept on disk, so each album or song is asked for once.

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"
)

type Artwork struct {
	dir     string
	enabled bool
	client  *http.Client

	mu      sync.Mutex
	busy    map[string]bool      // lookups in progress
	missing map[string]time.Time // when to ask again: not found → in 6 hours, network trouble → in a minute
}

func NewArtwork(dataDir string, enabled bool) *Artwork {
	dir := filepath.Join(dataDir, "artwork")
	os.MkdirAll(dir, 0755)
	return &Artwork{dir: dir, enabled: enabled, client: &http.Client{Timeout: 15 * time.Second},
		busy: map[string]bool{}, missing: map[string]time.Time{}}
}

func artKey(entity, artist, title string) string {
	h := sha1.Sum([]byte(entity + "|" + strings.ToLower(artist) + "|" + strings.ToLower(title)))
	return hex.EncodeToString(h[:10])
}

// artQuery is one way to look for a picture: entity "album" or "song".
type artQuery struct{ entity, artist, title string }

// Get returns the id of a picture already on disk; otherwise it starts a lookup in the
// background and returns "" (the remote asks every second, so it shows up a moment later).
// More queries are tried in order when the first finds nothing (album, then the song).
func (a *Artwork) Get(entity, artist, title string, more ...artQuery) string {
	if !a.enabled || strings.TrimSpace(artist+title) == "" {
		return ""
	}
	key := artKey(entity, artist, title)
	if _, err := os.Stat(a.Path(key)); err == nil {
		return key
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.busy[key] || time.Now().Before(a.missing[key]) {
		return ""
	}
	a.busy[key] = true
	go func() {
		err := errNoArt
		for _, q := range append([]artQuery{{entity, artist, title}}, more...) {
			if strings.TrimSpace(q.artist+q.title) == "" {
				continue
			}
			if err = a.fetch(key, q.entity, q.artist, q.title); err != errNoArt {
				break
			}
		}
		a.mu.Lock()
		delete(a.busy, key)
		if err != nil {
			wait := time.Minute
			if err == errNoArt {
				wait = 6 * time.Hour
			}
			a.missing[key] = time.Now().Add(wait)
		}
		a.mu.Unlock()
		if err != nil && err != errNoArt {
			log.Printf("artwork %q %q: %v", artist, title, err)
		}
	}()
	return ""
}

func (a *Artwork) Path(key string) string { return filepath.Join(a.dir, key+".jpg") }

var errNoArt = fmt.Errorf("not found")

func (a *Artwork) fetch(key, entity, artist, title string) error {
	// what to ask for: "artist title"; for songs also the title alone and the first of several
	// artists ("ЭММА М & НИКОЛАЕВ Игорь" finds nothing as it is)
	terms := []string{strings.TrimSpace(artist + " " + title)}
	if entity == "song" && artist != "" && title != "" {
		if first := reArtistSep.Split(artist, 2)[0]; first != artist {
			terms = append(terms, strings.TrimSpace(first+" "+title))
		}
		terms = append(terms, title)
	}
	var art string
	var netErr error
search:
	for _, term := range terms {
		// the Russian store knows Russian artists, the US one the rest
		for _, country := range []string{"ru", "us"} {
			u := "https://itunes.apple.com/search?limit=10&media=music&entity=" + entity + "&country=" + country + "&term=" + url.QueryEscape(term)
			var res struct {
				Results []struct {
					ArtistName string `json:"artistName"`
					ArtworkURL string `json:"artworkUrl100"`
				} `json:"results"`
			}
			if err := a.getJSON(u, &res); err != nil {
				netErr = err
				continue
			}
			for _, r := range res.Results {
				// iTunes always finds something; take it only if the artist is really the one asked for
				if r.ArtworkURL != "" && sameArtist(r.ArtistName, artist, term) {
					art = r.ArtworkURL
					break search
				}
			}
		}
	}
	if art == "" {
		if netErr != nil {
			return netErr
		}
		return errNoArt
	}
	// Apple's picture servers sometimes stall on https or answer 403; plain http gives the same
	// picture (the weather station learned that too). Big one first, the small one as a last resort.
	big := strings.Replace(art, "100x100bb", "600x600bb", 1)
	var err error
	for _, u := range []string{toHTTP(big), big, toHTTP(big), toHTTP(art), art} {
		if err = a.download(u, a.Path(key)); err == nil {
			return nil
		}
	}
	return err
}

func toHTTP(u string) string { return strings.Replace(u, "https://", "http://", 1) }

// download saves a JPEG picture; anything else (an error page) is refused.
func (a *Artwork) download(u, dst string) error {
	resp, err := a.client.Get(u)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("picture: HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if len(b) < 1000 || b[0] != 0xFF || b[1] != 0xD8 {
		return fmt.Errorf("picture: not a JPEG")
	}
	tmp := dst + ".tmp"
	if err := os.WriteFile(tmp, b, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

func (a *Artwork) getJSON(u string, v any) error {
	resp, err := a.client.Get(u)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("search: HTTP %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

func words(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
}

// sameArtist: the found artist matches the one we know (or, without a known artist,
// one of the found artist's words appears in what we searched for).
func sameArtist(found, artist, term string) bool {
	f := strings.Join(words(found), " ")
	if a := strings.Join(words(artist), " "); a != "" {
		if strings.Contains(f, a) || strings.Contains(a, f) {
			return true
		}
		// several artists or another order: "ЭММА М & НИКОЛАЕВ Игорь" / "Игорь Николаев, Эмма М"
		for _, w := range words(artist) {
			if len([]rune(w)) >= 4 && !commonWord[w] && strings.Contains(" "+f+" ", " "+w+" ") {
				return true
			}
		}
		return false
	}
	t := " " + strings.Join(words(term), " ") + " "
	for _, w := range words(found) {
		if len([]rune(w)) >= 3 && !commonWord[w] && strings.Contains(t, " "+w+" ") {
			return true
		}
	}
	return false
}

// words that say nothing about who the artist is
var commonWord = map[string]bool{"the": true, "and": true, "feat": true, "band": true, "group": true,
	"orchestra": true, "группа": true, "оркестр": true, "ансамбль": true, "various": true, "artists": true}

var reArtistSep = regexp.MustCompile(`(?i)\s*[&,;/]\s*|\s+(?:feat\.?|ft\.?|x|и|vs\.?)\s+`)

var (
	reBrackets = regexp.MustCompile(`[\[(\{][^\])\}]*[\])\}]`)
	reYear     = regexp.MustCompile(`(^|\s-\s)(19|20)\d\d(\s-\s|$)`)
	reDisc     = regexp.MustCompile(`(?i)^(cd|disc|disk|диск)\s*\d+$`)
)

// albumFromFolder guesses artist and album from a folder name like
// "Michael Jackson - Invincible (2001) [96-24]" or "Tarja - 2026 - Frisson Noir (24bit-48kHz)".
func albumFromFolder(rel string) (artist, album string) {
	name := filepath.Base(rel)
	if reDisc.MatchString(name) { // "Album/CD1": the album is the folder above
		name = filepath.Base(filepath.Dir(rel))
	}
	if name == "." || name == string(filepath.Separator) {
		return "", ""
	}
	name = strings.TrimSpace(reBrackets.ReplaceAllString(name, " "))
	name = reYear.ReplaceAllString(name, " - ")
	name = strings.Trim(strings.Join(strings.Fields(name), " "), " -")
	if i := strings.Index(name, " - "); i > 0 {
		return strings.TrimSpace(name[:i]), strings.Trim(name[i+3:], " -")
	}
	return "", name
}

var reTrackNo = regexp.MustCompile(`^\s*\d{1,3}[\s.\-_)]+`)

// songTitle strips the track number: "02 - Wenn das Liebe ist" -> "Wenn das Liebe ist".
func songTitle(t string) string { return strings.TrimSpace(reTrackNo.ReplaceAllString(t, "")) }

// songFromRadio splits the stream title "Artist - Title".
func songFromRadio(t string) (artist, title string) {
	if i := strings.Index(t, " - "); i > 0 {
		return strings.TrimSpace(t[:i]), strings.TrimSpace(reBrackets.ReplaceAllString(t[i+3:], " "))
	}
	return "", ""
}
