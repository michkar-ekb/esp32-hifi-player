package main

// CUE sheets: an album ripped as one big file (APE / FLAC / WAV) plus a .cue that marks where
// every song starts. The folder listing shows the songs instead of the big file; each song plays
// from its INDEX 01 to the next one, gapless.

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding/charmap"
)

type cueTrack struct {
	Num       int
	Title     string
	Performer string
	Start     float64 // seconds into the file
	End       float64 // 0 = to the end of the file
}

type cueSheet struct {
	Audio  string // the album file it refers to (full path)
	Tracks []cueTrack
}

type cueCacheEntry struct {
	key   string
	sheet *cueSheet
}

// parseCue reads a .cue that describes one album file with two or more songs.
// nil means "not usable": several files (one per song, already split) or the file is missing.
func (l *Library) parseCue(cuePath string) *cueSheet {
	st, err := os.Stat(cuePath)
	if err != nil {
		return nil
	}
	key := fmt.Sprintf("%d|%d", st.Size(), st.ModTime().Unix())
	l.mu.Lock()
	if c, ok := l.cueCache[cuePath]; ok && c.key == key {
		l.mu.Unlock()
		return c.sheet
	}
	l.mu.Unlock()

	sheet := readCue(cuePath)
	if sheet != nil && len(sheet.Tracks) > 0 && sheet.Tracks[len(sheet.Tracks)-1].End == 0 {
		// the last song ends with the file; its length is needed for the listing
		if d := l.fileDur(sheet.Audio); d > 0 {
			sheet.Tracks[len(sheet.Tracks)-1].End = d
		}
	}
	l.mu.Lock()
	l.cueCache[cuePath] = cueCacheEntry{key, sheet}
	l.mu.Unlock()
	return sheet
}

func (l *Library) fileDur(p string) float64 {
	st, err := os.Stat(p)
	if err != nil {
		return 0
	}
	return l.duration(p, st)
}

func readCue(cuePath string) *cueSheet {
	raw, err := os.ReadFile(cuePath)
	if err != nil {
		return nil
	}
	raw = bytes.TrimPrefix(raw, []byte("\xef\xbb\xbf"))
	if !utf8.Valid(raw) { // Russian rips are usually Windows-1251
		if dec, err := charmap.Windows1251.NewDecoder().Bytes(raw); err == nil {
			raw = dec
		}
	}
	var (
		files     []string
		tracks    []cueTrack
		albumPerf string
		cur       *cueTrack
	)
	sc := bufio.NewScanner(bytes.NewReader(raw))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		cmd, rest, _ := strings.Cut(line, " ")
		switch strings.ToUpper(cmd) {
		case "FILE":
			name := rest
			if i := strings.LastIndex(rest, " "); i > 0 { // drop the type: WAVE, MP3, ...
				name = rest[:i]
			}
			files = append(files, unquote(name))
		case "TRACK":
			f := strings.Fields(rest)
			n, _ := strconv.Atoi(f[0])
			tracks = append(tracks, cueTrack{Num: n, Start: -1})
			cur = &tracks[len(tracks)-1]
		case "TITLE":
			if cur != nil {
				cur.Title = unquote(rest)
			}
		case "PERFORMER":
			if cur != nil {
				cur.Performer = unquote(rest)
			} else {
				albumPerf = unquote(rest)
			}
		case "INDEX":
			f := strings.Fields(rest)
			if cur != nil && len(f) == 2 && f[0] == "01" {
				cur.Start = cueTime(f[1])
			}
		}
	}
	if len(files) != 1 || len(tracks) < 2 {
		return nil
	}
	audio := findCueAudio(filepath.Dir(cuePath), files[0])
	if audio == "" {
		return nil
	}
	for i := range tracks {
		if tracks[i].Start < 0 {
			return nil
		}
		if tracks[i].Performer == "" {
			tracks[i].Performer = albumPerf
		}
		if i+1 < len(tracks) {
			tracks[i].End = tracks[i+1].Start
		}
	}
	return &cueSheet{Audio: audio, Tracks: tracks}
}

// cueTime parses mm:ss:ff (75 frames per second).
func cueTime(s string) float64 {
	p := strings.Split(s, ":")
	if len(p) != 3 {
		return -1
	}
	m, _ := strconv.Atoi(p[0])
	sec, _ := strconv.Atoi(p[1])
	fr, _ := strconv.Atoi(p[2])
	return float64(m*60+sec) + float64(fr)/75
}

func unquote(s string) string { return strings.Trim(strings.TrimSpace(s), `"`) }

// findCueAudio finds the file a cue refers to; rips often say "album.wav" next to "album.ape".
func findCueAudio(dir, name string) string {
	name = filepath.Base(filepath.FromSlash(strings.ReplaceAll(name, `\`, "/")))
	if p := filepath.Join(dir, name); fileExists(p) && isAudio(p) { // SACD Extract writes cues that point at themselves
		return p
	}
	base := strings.ToLower(titleOf(name))
	des, _ := os.ReadDir(dir)
	for _, de := range des {
		if !de.IsDir() && isAudio(de.Name()) && strings.ToLower(titleOf(de.Name())) == base {
			return filepath.Join(dir, de.Name())
		}
	}
	return ""
}

// cue track paths look like "Album/album.cue#3"
func splitCuePath(rel string) (cue string, num int, ok bool) {
	i := strings.LastIndex(rel, ".cue#")
	if i < 0 {
		return "", 0, false
	}
	n, err := strconv.Atoi(rel[i+5:])
	if err != nil {
		return "", 0, false
	}
	return rel[:i+4], n, true
}

func (t cueTrack) label() string {
	title := t.Title
	if title == "" {
		title = fmt.Sprintf("Трек %d", t.Num)
	}
	return fmt.Sprintf("%02d. %s", t.Num, title)
}

// cueItems turns songs of a sheet into queue items (num 0 = all songs).
func (l *Library) cueItems(cueRel string, num int) ([]Item, error) {
	full, err := l.Abs(cueRel)
	if err != nil {
		return nil, err
	}
	sh := l.parseCue(full)
	if sh == nil {
		return nil, fmt.Errorf("не удалось прочитать %s", filepath.Base(full))
	}
	var items []Item
	for _, t := range sh.Tracks {
		if num != 0 && t.Num != num {
			continue
		}
		it := Item{Kind: "file", Path: l.relOf(sh.Audio), Title: t.label(), Start: t.Start, End: t.End}
		if t.End > t.Start {
			it.Dur = t.End - t.Start
		}
		items = append(items, it)
	}
	return items, nil
}

// clipSource stops a song at the start of the next one.
type clipSource struct {
	Source
	left int64 // frames still to play
}

func (c *clipSource) Read(p []byte) (int, error) {
	if c.left <= 0 {
		return 0, io.EOF
	}
	fs := c.Format().FrameSize()
	if max := c.left * int64(fs); int64(len(p)) > max {
		p = p[:max]
	}
	n, err := c.Source.Read(p)
	c.left -= int64(n / fs)
	return n, err
}
