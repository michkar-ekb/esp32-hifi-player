package main

// SACD disc images (.iso, "Scarletbook"). Written from the published layout of the format:
//   sector 510: master TOC "SACDMTOC" -> where the stereo area TOC is
//   stereo area TOC "TWOCHTOC" + following sectors: track list (SACDTRL1 sectors, SACDTRL2 times),
//   track titles (SACDTTxt)
//   audio sectors: 1-byte header, packet infos, frame infos, packets; 75 frames per second.
// Plain DSD frames go through our own DSD filter; DST-compressed frames are decoded by ffmpeg.
// Only the stereo area is used.

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"golang.org/x/text/encoding/charmap"
)

const (
	sacdSector     = 2048
	sacdFrameRate  = 75
	sacdFrameBytes = 4704 // one channel of one DSD64 frame (1/75 s)
)

type sacdTrack struct {
	Num       int
	Title     string
	Performer string
	StartLSN  uint32
	LenLSN    uint32
	StartFr   uint32 // in 1/75 s frames, from the start of the area
	DurFr     uint32
}

type sacdDisc struct {
	Path     string
	DST      bool
	Channels int
	AreaLSN  [2]uint32 // first and last audio sector of the stereo area
	Tracks   []sacdTrack
}

func (t sacdTrack) seconds() float64 { return float64(t.DurFr) / sacdFrameRate }

func readSectors(f io.ReaderAt, lsn uint32, n int) ([]byte, error) {
	b := make([]byte, n*sacdSector)
	_, err := f.ReadAt(b, int64(lsn)*sacdSector)
	return b, err
}

var (
	sacdCacheMu sync.Mutex
	sacdCache   = map[string]sacdCacheEntry{}
)

type sacdCacheEntry struct {
	key  string
	disc *sacdDisc
	err  error
}

// parseSACD reads the table of contents of a SACD image (cached by size and mtime).
func parseSACD(path string) (*sacdDisc, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	key := fmt.Sprintf("%d|%d", st.Size(), st.ModTime().Unix())
	sacdCacheMu.Lock()
	if c, ok := sacdCache[path]; ok && c.key == key {
		sacdCacheMu.Unlock()
		return c.disc, c.err
	}
	sacdCacheMu.Unlock()
	d, err := readSACD(path)
	sacdCacheMu.Lock()
	sacdCache[path] = sacdCacheEntry{key, d, err}
	sacdCacheMu.Unlock()
	return d, err
}

var errNotSACD = errors.New("not a SACD image")

func readSACD(path string) (*sacdDisc, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	be := binary.BigEndian
	m, err := readSectors(f, 510, 1)
	if err != nil || string(m[0:8]) != "SACDMTOC" {
		return nil, errNotSACD
	}
	areaStart, areaSize := be.Uint32(m[64:]), int(be.Uint16(m[84:]))
	if areaStart == 0 || areaSize == 0 {
		return nil, errors.New("SACD: no stereo area on this disc")
	}
	a, err := readSectors(f, areaStart, areaSize)
	if err != nil || string(a[0:8]) != "TWOCHTOC" {
		return nil, errors.New("SACD: bad stereo area TOC")
	}
	d := &sacdDisc{Path: path, DST: a[21]&0x0F == 0, Channels: int(a[32])}
	d.AreaLSN = [2]uint32{be.Uint32(a[72:]), be.Uint32(a[76:])}
	count := int(a[69])
	charset := a[90] & 0x07 // languages[0].character_set
	if count == 0 || count > 255 || d.Channels != 2 {
		return nil, fmt.Errorf("SACD: unexpected stereo area (%d tracks, %d channels)", count, d.Channels)
	}
	d.Tracks = make([]sacdTrack, count)
	for i := range d.Tracks {
		d.Tracks[i].Num = i + 1
	}
	gotText := false
	for s := 1; s < areaSize; s++ {
		p := a[s*sacdSector:] // text positions may point into the following sectors
		switch string(p[0:8]) {
		case "SACDTRL1":
			for i := 0; i < count; i++ {
				d.Tracks[i].StartLSN = be.Uint32(p[8+4*i:])
				d.Tracks[i].LenLSN = be.Uint32(p[8+4*255+4*i:])
			}
		case "SACDTRL2":
			for i := 0; i < count; i++ {
				st, du := p[8+4*i:], p[8+4*255+4*i:]
				d.Tracks[i].StartFr = (uint32(st[0])*60+uint32(st[1]))*sacdFrameRate + uint32(st[2])
				d.Tracks[i].DurFr = (uint32(du[0])*60+uint32(du[1]))*sacdFrameRate + uint32(du[2])
			}
		case "SACDTTxt":
			if gotText { // the first text channel only
				continue
			}
			gotText = true
			for i := 0; i < count; i++ {
				pos := int(be.Uint16(p[8+2*i:]))
				if pos <= 0 || pos >= len(p) {
					continue
				}
				items := int(p[pos])
				q := pos + 4
				for j := 0; j < items && q+2 < len(p); j++ {
					typ := p[q]
					q += 2 // type, then one unused byte
					end := q
					for end < len(p) && p[end] != 0 {
						end++
					}
					txt := sacdText(p[q:end], charset)
					switch typ {
					case 0x01:
						d.Tracks[i].Title = txt
					case 0x02:
						d.Tracks[i].Performer = txt
					}
					q = end
					for q < len(p) && p[q] == 0 {
						q++
					}
				}
			}
		}
	}
	for _, t := range d.Tracks {
		if t.LenLSN == 0 {
			return nil, errors.New("SACD: track list not found")
		}
	}
	return d, nil
}

func sacdText(b []byte, charset byte) string {
	if charset == 2 || charset == 7 { // ISO 8859-1
		if s, err := charmap.ISO8859_1.NewDecoder().Bytes(b); err == nil {
			return strings.TrimSpace(string(s))
		}
	}
	return strings.TrimSpace(string(b))
}

func (t sacdTrack) label() string {
	title := t.Title
	if title == "" {
		title = fmt.Sprintf("Трек %d", t.Num)
	}
	return fmt.Sprintf("%02d. %s", t.Num, title)
}

// splitISOPath: SACD songs look like "Album/disc.iso#3"
func splitISOPath(rel string) (iso string, num int, ok bool) {
	i := strings.LastIndex(strings.ToLower(rel), ".iso#")
	if i < 0 {
		return "", 0, false
	}
	var n int
	if _, err := fmt.Sscanf(rel[i+5:], "%d", &n); err != nil {
		return "", 0, false
	}
	return rel[:i+4], n, true
}

// ---------- audio ----------

// sacdFrames walks the audio sectors of one track and returns whole DSD frames
// (byte-interleaved channels, MSB first) with their time codes.
type sacdFrames struct {
	r        io.ReadSeeker
	lsn, end uint32
	sec      []byte
	cur      []byte // frame being assembled
	curTC    uint32
	started  bool
	ready    [][]byte
	readyTC  []uint32
	channels int
}

func newSACDFrames(r io.ReadSeeker, from, end uint32, channels int) (*sacdFrames, error) {
	if _, err := r.Seek(int64(from)*sacdSector, io.SeekStart); err != nil {
		return nil, err
	}
	return &sacdFrames{r: r, lsn: from, end: end, sec: make([]byte, sacdSector), channels: channels}, nil
}

func tcFrames(b []byte) uint32 {
	return (uint32(b[0])*60+uint32(b[1]))*sacdFrameRate + uint32(b[2])
}

// next returns the next complete frame and its time code (area frames).
func (s *sacdFrames) next() ([]byte, uint32, error) {
	full := s.channels * sacdFrameBytes
	for len(s.ready) == 0 {
		if s.lsn >= s.end {
			if s.started && len(s.cur) == full {
				s.started = false
				return s.cur, s.curTC, nil
			}
			return nil, 0, io.EOF
		}
		if _, err := io.ReadFull(s.r, s.sec); err != nil {
			return nil, 0, err
		}
		s.lsn++
		b := s.sec
		h := b[0]
		if h&1 == 1 {
			return nil, 0, errSACDDST
		}
		pcount, fcount := int(h>>5), int(h>>2)&7
		off := 1
		type pkt struct {
			start bool
			typ   int
			n     int
		}
		pk := make([]pkt, pcount)
		for i := range pk {
			v := int(b[off])<<8 | int(b[off+1])
			pk[i] = pkt{v>>15&1 == 1, v >> 11 & 7, v & 0x7FF}
			off += 2
		}
		tcs := make([]uint32, fcount)
		for i := range tcs {
			tcs[i] = tcFrames(b[off:])
			off += 3 // plain DSD: time code only
		}
		fi := 0
		for _, p := range pk {
			if off+p.n > len(b) {
				break // damaged sector
			}
			data := b[off : off+p.n]
			off += p.n
			if p.typ != 2 { // only audio packets
				continue
			}
			if p.start {
				if s.started && len(s.cur) == full {
					s.ready = append(s.ready, s.cur)
					s.readyTC = append(s.readyTC, s.curTC)
				}
				s.cur = make([]byte, 0, full)
				s.started = true
				if fi < len(tcs) {
					s.curTC = tcs[fi]
				}
				fi++
			}
			if s.started && len(s.cur)+len(data) <= full {
				s.cur = append(s.cur, data...)
			}
		}
	}
	f, tc := s.ready[0], s.readyTC[0]
	s.ready, s.readyTC = s.ready[1:], s.readyTC[1:]
	return f, tc, nil
}

var errSACDDST = errors.New("на этом SACD звук сжат DST — такие образы пока не играют")

// openSACDTrack plays one song of a SACD image through the DSD-to-PCM filter.
func openSACDTrack(path string, num int) (Source, error) {
	d, err := parseSACD(path)
	if err != nil {
		return nil, err
	}
	if num < 1 || num > len(d.Tracks) {
		return nil, fmt.Errorf("SACD: no track %d", num)
	}
	if d.DST {
		return nil, errSACDDST
	}
	t := d.Tracks[num-1]
	pf, err := openPrefetch(path)
	if err != nil {
		return nil, err
	}
	s := &dsdSource{f: pf, dsdRate: 2822400, inCh: d.Channels, totalB: int64(t.DurFr) * sacdFrameBytes, msbFirst: true}
	var fr *sacdFrames
	var pend []byte                       // bytes of the current frame not handed out yet (interleaved)
	restart := func(frame uint32) error { // position at the frame with this index from the start of the song
		lsn := sacdFindSector(path, t, t.StartFr+frame)
		var err error
		if fr, err = newSACDFrames(pf, lsn, t.StartLSN+t.LenLSN, d.Channels); err != nil {
			return err
		}
		pend = nil
		for { // drop frames before the target
			f, tc, err := fr.next()
			if err != nil {
				return err
			}
			if tc >= t.StartFr+frame {
				pend = f
				return nil
			}
		}
	}
	if err := restart(0); err != nil && err != io.EOF {
		pf.Close()
		return nil, err
	}
	s.readCh = func(n int, out [][]byte) (int, error) {
		done := 0
		ch := d.Channels
		for done < n {
			if len(pend) == 0 {
				f, _, err := fr.next()
				if err != nil {
					return done, err
				}
				pend = f
			}
			k := min(n-done, len(pend)/ch)
			for i := 0; i < k; i++ {
				for c := 0; c < ch; c++ {
					out[c][done+i] = pend[i*ch+c]
				}
			}
			pend = pend[k*ch:]
			done += k
		}
		return done, nil
	}
	s.seekB = func(b int64) error {
		if err := restart(uint32(b / sacdFrameBytes)); err != nil && err != io.EOF {
			return err
		}
		skip := int(b%sacdFrameBytes) * d.Channels
		if skip <= len(pend) {
			pend = pend[skip:]
		}
		return nil
	}
	return s.finish()
}

// sacdFindSector finds the sector where the frame with time code tc starts (binary search over
// the time codes in sector headers). Falls back to the start of the song.
func sacdFindSector(path string, t sacdTrack, tc uint32) uint32 {
	if tc <= t.StartFr {
		return t.StartLSN
	}
	f, err := os.Open(path)
	if err != nil {
		return t.StartLSN
	}
	defer f.Close()
	b := make([]byte, 64)
	first := func(lsn uint32) (uint32, bool) { // first frame time code in this sector
		if _, err := f.ReadAt(b, int64(lsn)*sacdSector); err != nil {
			return 0, false
		}
		h := b[0]
		pcount, fcount := int(h>>5), int(h>>2)&7
		if fcount == 0 {
			return 0, false
		}
		return tcFrames(b[1+2*pcount:]), true
	}
	lo, hi := t.StartLSN, t.StartLSN+t.LenLSN
	for hi-lo > 8 {
		mid := lo + (hi-lo)/2
		m := mid
		var v uint32
		var ok bool
		for ; m < hi; m++ { // nearest sector that starts a frame
			if v, ok = first(m); ok {
				break
			}
		}
		if !ok || v >= tc {
			hi = mid
		} else {
			lo = mid
		}
	}
	if lo > t.StartLSN+2 {
		lo -= 2 // a frame may begin a sector or two before the one that lists it
	}
	return lo
}
