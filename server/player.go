package main

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Item is one entry of the play queue.
type Item struct {
	Kind  string  `json:"kind"`            // "file" | "radio"
	Path  string  `json:"path,omitempty"`  // relative to the music root
	URL   string  `json:"url,omitempty"`   // radio stream
	Title string  `json:"title"`           // shown in the UI
	Dur   float64 `json:"dur,omitempty"`   // seconds, 0 = unknown
	Start float64 `json:"start,omitempty"` // CUE song: where it starts in the album file
	End   float64 `json:"end,omitempty"`   // CUE song: where it ends (0 = end of file)
	Err   string  `json:"err,omitempty"`   // why it could not be played
}

// segment marks where a queue item starts inside the PCM stream of the current epoch.
type segment struct {
	idx    int     // queue index
	start  uint64  // frame number in the epoch stream
	offset float64 // seek position of the item at start, seconds
	rate   int
	label  string // "FLAC · 44,1 кГц · 16 бит"
}

// Player is the single source of truth: the queue, what is playing and what the
// hardware player (ESP32) has reported. Every change of "what is heard" (play, next,
// seek, stop) starts a new epoch: the ESP drops its buffer and reconnects.
type Player struct {
	mu   sync.Mutex
	cond *sync.Cond
	lib  *Library

	Queue  []Item `json:"queue"`
	Cur    int    `json:"cur"` // what the ESP is playing now (-1 = nothing)
	Volume int    `json:"volume"`
	state  string // "stop" | "play" | "pause"
	epoch  uint32

	// stream producer (runs ahead of Cur by the size of the ESP buffer)
	src       Source
	srcIdx    int
	srcOffset float64
	srcDone   bool // queue exhausted, nothing more to send
	segs      []segment
	sent      uint64 // frames sent in this epoch
	fan       *fanout

	// reported by the ESP
	played   uint64
	lastPoll time.Time
	bufMs    int
	rssi     int
	kbps     int

	radioTitle string
	statePath  string
	saveTimer  *time.Timer
}

func NewPlayer(lib *Library, dataDir string) *Player {
	p := &Player{lib: lib, Cur: -1, Volume: 70, state: "stop", statePath: filepath.Join(dataDir, "state.json")}
	p.cond = sync.NewCond(&p.mu)
	if b, err := os.ReadFile(p.statePath); err == nil {
		json.Unmarshal(b, p)
		if p.Cur >= len(p.Queue) {
			p.Cur = len(p.Queue) - 1
		}
	}
	go func() { // wake streams regularly so they notice closed connections
		for range time.Tick(time.Second) {
			p.cond.Broadcast()
		}
	}()
	return p
}

func (p *Player) saveLocked() {
	if p.saveTimer != nil {
		p.saveTimer.Stop()
	}
	b, _ := json.MarshalIndent(struct {
		Queue  []Item `json:"queue"`
		Cur    int    `json:"cur"`
		Volume int    `json:"volume"`
	}{p.Queue, p.Cur, p.Volume}, "", " ")
	p.saveTimer = time.AfterFunc(500*time.Millisecond, func() {
		tmp := p.statePath + ".tmp"
		if os.WriteFile(tmp, b, 0644) == nil {
			os.Rename(tmp, p.statePath)
		}
	})
}

// startLocked begins a new epoch playing queue[i] from offset seconds.
func (p *Player) startLocked(i int, offset float64) {
	p.closeSrcLocked()
	p.dropFanLocked()
	p.epoch++
	p.Cur, p.srcIdx, p.srcOffset = i, i, offset
	p.srcDone, p.segs, p.sent, p.played = false, nil, 0, 0
	p.radioTitle = ""
	p.state = "play"
	if i < 0 || i >= len(p.Queue) {
		p.state, p.Cur = "stop", -1
	}
	p.saveLocked()
	p.cond.Broadcast()
}

func (p *Player) closeSrcLocked() {
	if p.src != nil {
		p.src.Close()
		p.src = nil
	}
}

func (p *Player) stopLocked() {
	p.closeSrcLocked()
	p.dropFanLocked()
	p.epoch++
	p.state = "stop"
	p.segs, p.sent, p.played, p.srcDone = nil, 0, 0, false
	p.cond.Broadcast()
}

// positionLocked: queue index and seconds actually heard, from the ESP report.
func (p *Player) positionLocked() (int, float64) {
	if len(p.segs) == 0 {
		return p.Cur, p.srcOffset
	}
	s := p.segs[0]
	for _, x := range p.segs {
		if x.start <= p.played {
			s = x
		}
	}
	return s.idx, s.offset + float64(p.played-s.start)/float64(s.rate)
}

// ---------- commands from the web remote ----------

func (p *Player) PlayNow(items []Item) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(items) == 0 {
		return
	}
	at := len(p.Queue)
	if p.Cur >= 0 {
		at = p.Cur + 1
	}
	p.Queue = append(p.Queue[:at], append(items, p.Queue[at:]...)...)
	p.startLocked(at, 0)
}

func (p *Player) Add(items []Item) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.Queue = append(p.Queue, items...)
	// the producer may already have finished the old queue: let it continue
	if p.srcDone && p.state != "stop" {
		p.srcDone = false
		p.srcIdx = len(p.Queue) - len(items)
		p.cond.Broadcast()
	}
	p.saveLocked()
	return len(p.Queue)
}

func (p *Player) Command(cmd string, arg float64) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	idx, pos := p.positionLocked()
	switch cmd {
	case "pause":
		if p.state == "play" {
			p.state = "pause"
		}
	case "resume", "play":
		switch p.state {
		case "pause":
			p.state = "play"
		case "stop":
			i := p.Cur
			if i < 0 {
				i = 0
			}
			p.startLocked(i, 0)
		}
	case "toggle":
		if p.state == "play" {
			p.state = "pause"
		} else {
			p.mu.Unlock()
			err := p.Command("resume", 0)
			p.mu.Lock()
			return err
		}
	case "stop":
		p.stopLocked()
	case "next":
		if idx+1 < len(p.Queue) {
			p.startLocked(idx+1, 0)
		} else {
			p.Cur = idx
			p.stopLocked()
		}
	case "prev":
		if pos > 3 || idx <= 0 {
			p.startLocked(max(idx, 0), 0)
		} else {
			p.startLocked(idx-1, 0)
		}
	case "jump":
		p.startLocked(int(arg), 0)
	case "seek":
		if idx < 0 || idx >= len(p.Queue) || p.Queue[idx].Kind == "radio" {
			return ErrNoSeek
		}
		if arg < 0 {
			arg = 0
		}
		p.startLocked(idx, arg)
	case "skip": // relative seek, arg in seconds
		p.mu.Unlock()
		err := p.Command("seek", pos+arg)
		p.mu.Lock()
		return err
	case "volume":
		p.Volume = int(max(0, min(100, arg)))
		p.saveLocked()
	case "remove":
		i := int(arg)
		if i < 0 || i >= len(p.Queue) {
			return errors.New("bad index")
		}
		p.Queue = append(p.Queue[:i], p.Queue[i+1:]...)
		switch {
		case i < idx:
			p.Cur, p.srcIdx = idx-1, p.srcIdx-1
			for k := range p.segs {
				p.segs[k].idx--
			}
		case i == idx && p.state != "stop":
			p.startLocked(i, 0) // plays what moved into this place
		case i > idx && i <= p.srcIdx && p.state != "stop":
			p.startLocked(idx, pos) // a prefetched track was removed: restart from here
		case i == idx:
			p.Cur = min(i, len(p.Queue)-1)
		}
		p.saveLocked()
	case "clear": // keep only what is playing
		if idx >= 0 && idx < len(p.Queue) && p.state != "stop" {
			p.Queue = []Item{p.Queue[idx]}
			p.startLocked(0, pos)
		} else {
			p.Queue, p.Cur = nil, -1
			p.stopLocked()
		}
		p.saveLocked()
	default:
		return errors.New("unknown command")
	}
	p.cond.Broadcast()
	return nil
}

// ---------- state for the web remote ----------

type PlayerState struct {
	State    string  `json:"state"`
	Cur      int     `json:"cur"`
	Pos      float64 `json:"pos"`
	Dur      float64 `json:"dur"`
	Title    string  `json:"title"`
	Sub      string  `json:"sub"`
	Cover    string  `json:"cover"`
	Kind     string  `json:"kind"`
	Volume   int     `json:"volume"`
	Queue    []Item  `json:"queue"`
	Online   bool    `json:"online"`
	BufferMs int     `json:"bufferMs"`
	RSSI     int     `json:"rssi"`
	Kbps     int     `json:"kbps"`
	Format   string  `json:"format"`
}

func (p *Player) State() PlayerState {
	st, coverDir := p.stateLocked()
	if coverDir != "" { // touches the (possibly slow) music folder: outside the lock
		if dir, err := p.lib.Abs(coverDir); err == nil && p.lib.hasCover(dir) {
			st.Cover = "/api/cover?path=" + urlQuery(coverDir)
		}
	}
	return st
}

func (p *Player) stateLocked() (PlayerState, string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	coverDir := ""
	idx, pos := p.positionLocked()
	st := PlayerState{State: p.state, Cur: idx, Pos: pos, Volume: p.Volume, Queue: p.Queue,
		Online: time.Since(p.lastPoll) < 3*time.Second, BufferMs: p.bufMs, RSSI: p.rssi, Kbps: p.kbps}
	if idx >= 0 && idx < len(p.Queue) {
		it := p.Queue[idx]
		st.Title, st.Dur, st.Kind = it.Title, it.Dur, it.Kind
		if it.Kind == "file" {
			if dir := filepath.Dir(it.Path); dir != "." {
				st.Sub = filepath.Base(dir)
			}
			coverDir = filepath.Dir(it.Path)
		} else {
			st.Sub = p.radioTitle
		}
	}
	for _, s := range p.segs {
		if s.idx == idx {
			st.Format = s.label
		}
	}
	st.Queue = append([]Item(nil), p.Queue...) // copy: encoded after the lock is released
	return st, coverDir
}

// ---------- ESP side ----------

// Poll is called by the ESP a few times per second; returns epoch, state and volume.
func (p *Player) Poll(epoch uint32, played uint64, bufMs, rssi, kbps int, resync bool) (uint32, string, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.lastPoll, p.bufMs, p.rssi, p.kbps = time.Now(), bufMs, rssi, kbps
	if epoch == p.epoch {
		p.played = played
		if resync && p.state != "stop" { // a lane broke: restart from what has been heard
			idx, pos := p.positionLocked()
			if idx >= 0 && idx < len(p.Queue) && p.Queue[idx].Kind == "radio" {
				pos = 0
			}
			log.Printf("player asked to resync at %d %.1f s", idx, pos)
			st := p.state
			p.startLocked(idx, pos)
			p.state = st
			return p.epoch, p.state, p.Volume
		}
		idx, _ := p.positionLocked()
		if idx != p.Cur {
			p.Cur = idx
			p.saveLocked()
		}
		if p.srcDone && p.state == "play" && played >= p.sent && p.sent > 0 {
			p.stopLocked() // the last track has been heard to the end
		}
	}
	return p.epoch, p.state, p.Volume
}

const (
	chunkBytes = 4096 // payload per chunk: fits the ESP32 TCP window (5.7 KB) with room to spare
	aheadBytes = 1 << 20
)

// fanout spreads the chunks of one epoch over several TCP connections ("lanes").
// The ESP32's TCP window is fixed at 5.7 KB, so one connection tops out at ~5 Mbit/s
// on a Wi-Fi link with 8 ms RTT; four lanes give ~4x that. Chunk n goes to lane n % lanes,
// and the player reads the lanes in the same round-robin order.
type fanout struct {
	epoch uint32
	lanes int
	ch    []chan []byte
	done  chan struct{}
}

func (p *Player) dropFanLocked() {
	if p.fan != nil {
		close(p.fan.done)
		p.fan = nil
	}
}

// Lane serves one connection of the player for an epoch until the epoch changes or the client goes away.
// Chunk: "S3P2" | rate u32 | bits u8 | channels u8 | flags u8 (1 = new track) | 0 | length u32 | seq u32 | data
func (p *Player) Lane(epoch uint32, lane, lanes int, w io.Writer, flush func(), gone <-chan struct{}) {
	if lanes < 1 || lanes > 8 || lane < 0 || lane >= lanes {
		return
	}
	p.mu.Lock()
	if p.epoch != epoch {
		p.mu.Unlock()
		return
	}
	if p.fan == nil || p.fan.epoch != epoch || p.fan.lanes != lanes {
		p.dropFanLocked()
		f := &fanout{epoch: epoch, lanes: lanes, done: make(chan struct{})}
		for i := 0; i < lanes; i++ {
			f.ch = append(f.ch, make(chan []byte, aheadBytes/chunkBytes/lanes))
		}
		p.fan = f
		go p.produce(f)
	}
	f := p.fan
	p.mu.Unlock()
	for {
		select {
		case b := <-f.ch[lane]:
			if _, err := w.Write(b); err != nil {
				return
			}
			if len(f.ch[lane]) == 0 {
				flush()
			}
		case <-f.done:
			return
		case <-gone:
			return
		}
	}
}

// produce decodes the queue of the epoch into numbered chunks and deals them out to the lanes.
func (p *Player) produce(f *fanout) {
	buf := make([]byte, chunkBytes)
	newTrack := true
	var seq uint32
	for {
		p.mu.Lock()
		for p.epoch == f.epoch && p.fan == f && (p.state != "play" && p.state != "pause" || p.srcDone) {
			p.cond.Wait()
		}
		if p.epoch != f.epoch || p.fan != f {
			p.mu.Unlock()
			return
		}
		if p.src == nil {
			if !p.openNextLocked() {
				p.mu.Unlock()
				continue
			}
			newTrack = true
		}
		src := p.src
		fm := src.Format()
		p.mu.Unlock()

		n, err := src.Read(buf[:chunkBytes/fm.FrameSize()*fm.FrameSize()])

		p.mu.Lock()
		if p.epoch != f.epoch || p.src != src {
			p.mu.Unlock()
			return
		}
		if n > 0 {
			if newTrack {
				p.segs = append(p.segs, segment{idx: p.srcIdx, start: p.sent, offset: p.srcOffset, rate: fm.Rate, label: fmtLabel(p.Queue[p.srcIdx], fm)})
			}
			p.sent += uint64(n / fm.FrameSize())
		}
		if err != nil {
			if err != io.EOF {
				log.Printf("read %s: %v", p.Queue[p.srcIdx].Title, err)
			}
			p.closeSrcLocked()
			p.srcIdx++
			p.srcOffset = 0
		}
		p.mu.Unlock()

		if n > 0 {
			c := make([]byte, 20+n)
			copy(c[0:4], "S3P2")
			binary.LittleEndian.PutUint32(c[4:], uint32(fm.Rate))
			c[8], c[9] = byte(fm.Bits), byte(fm.Channels)
			if newTrack {
				c[10] = 1
			}
			binary.LittleEndian.PutUint32(c[12:], uint32(n))
			binary.LittleEndian.PutUint32(c[16:], seq)
			copy(c[20:], buf[:n])
			select {
			case f.ch[int(seq)%f.lanes] <- c:
			case <-f.done:
				return
			}
			seq++
			newTrack = false
		}
	}
}

// openNextLocked opens queue[srcIdx]; skips items that fail. False when nothing was opened.
func (p *Player) openNextLocked() bool {
	for p.srcIdx < len(p.Queue) {
		it := p.Queue[p.srcIdx] // copy: the queue may change while the file opens
		var src Source
		var err error
		epoch := p.epoch
		p.mu.Unlock() // opening a radio stream can take seconds
		if it.Kind == "radio" {
			src, err = openRadio(it.URL, func(t string) {
				p.mu.Lock()
				if p.epoch == epoch {
					p.radioTitle = t
				}
				p.mu.Unlock()
			})
		} else {
			var full string
			full, err = p.lib.Abs(it.Path)
			if err == nil {
				src, err = openFile(full)
			}
		}
		if err == nil && it.Start+p.srcOffset > 0 {
			if e := src.Seek(it.Start + p.srcOffset); e != nil {
				log.Printf("seek: %v", e)
				p.srcOffset = 0
			}
		}
		if err == nil && it.End > 0 { // a CUE song: stop where the next one begins
			left := (it.End - it.Start - p.srcOffset) * float64(src.Format().Rate)
			src = &clipSource{Source: src, left: int64(left)}
		}
		p.mu.Lock()
		if p.epoch != epoch {
			if src != nil {
				src.Close()
			}
			return false
		}
		if p.srcIdx >= len(p.Queue) {
			if src != nil {
				src.Close()
			}
			break
		}
		if err != nil {
			log.Printf("open %s: %v", it.Title, err)
			p.Queue[p.srcIdx].Err = err.Error()
			p.srcIdx++
			p.srcOffset = 0
			continue
		}
		p.Queue[p.srcIdx].Err = ""
		if d := src.Duration(); d > 0 && it.Start == 0 && it.End == 0 {
			p.Queue[p.srcIdx].Dur = d
		}
		p.src = src
		return true
	}
	p.srcDone = true
	if p.sent == 0 { // nothing playable at all
		p.state = "stop"
	}
	p.cond.Broadcast()
	return false
}
