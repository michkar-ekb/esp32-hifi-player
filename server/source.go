package main

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hajimehoshi/go-mp3"
	"github.com/mewkiz/flac/frame"
)

// Format of the PCM a Source produces: interleaved little-endian signed samples.
// Bits is 16 or 24 (24-bit samples are packed in 3 bytes).
type Format struct {
	Rate     int
	Bits     int
	Channels int
	Codec    string // set when ffmpeg decodes something unusual, e.g. "dts"
	SrcRate  int    // original rate when the server resamples (above 96 kHz)
}

func (f Format) FrameSize() int { return f.Bits / 8 * f.Channels }

// Source turns a file or a stream into PCM.
type Source interface {
	Format() Format
	Read(p []byte) (int, error) // fills whole frames only
	Duration() float64          // seconds, 0 when unknown (radio)
	Seek(sec float64) error
	Close() error
}

var ErrNoSeek = errors.New("seek is not supported")

var audioExt = map[string]bool{".flac": true, ".mp3": true, ".wav": true, ".dsf": true, ".dff": true}

func isAudio(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	return audioExt[ext] || ffmpegExt[ext]
}

// openFile opens a track for playback, reading the file ahead in the background.
// What the built-in decoders cannot handle goes to ffmpeg, if it is installed.
func openFile(path string) (Source, error) {
	s, err := openNative(path, openPrefetchRSC)
	if err == nil && s.Format().Rate > maxRate && haveFFmpeg() { // too much for the player's Wi-Fi: resample
		s.Close()
		return openFFmpeg(path)
	}
	if err == nil {
		return s, nil
	}
	if haveFFmpeg() {
		if fs, ferr := openFFmpeg(path); ferr == nil {
			return fs, nil
		}
	} else if err == errCompressedWAV || err == errNoFFmpeg {
		return nil, fmt.Errorf("%v: установите ffmpeg", err)
	}
	return nil, err
}

// openNative uses the built-in decoders only (plain os.Open for header-only reads).
func openNative(path string, open opener) (Source, error) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".flac":
		return openFLAC(path, open)
	case ".mp3":
		return openMP3File(path, open)
	case ".wav":
		return openWAV(path, open)
	case ".dsf", ".dff":
		return openDSD(path, open)
	}
	if ffmpegExt[strings.ToLower(filepath.Ext(path))] {
		return nil, errNoFFmpeg
	}
	return nil, fmt.Errorf("unsupported file: %s", filepath.Base(path))
}

type opener func(string) (io.ReadSeekCloser, error)

func openPlain(path string) (io.ReadSeekCloser, error)       { return os.Open(path) }
func openPrefetchRSC(path string) (io.ReadSeekCloser, error) { return openPrefetch(path) }

// fileDuration reads only the header; used by the folder browser.
func fileDuration(path string) float64 {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".mp3":
		return 0 // would need a full scan; shown when the track plays
	}
	s, err := openNative(path, openPlain)
	if err != nil {
		if haveFFmpeg() {
			return ffDuration(path)
		}
		return 0
	}
	defer s.Close()
	return s.Duration()
}

// ---------- FLAC ----------
//
// Frames are read with our own loop (frame.Parse decodes a frame and checks its CRC-16).
// Seeking does not rely on a SEEKTABLE: many rips have none, and then the library would
// scan the whole file (hundreds of MB over a network share) before every seek. We bisect
// the file instead: jump to an offset, find the next valid frame, compare its sample number.

type flacSource struct {
	path      string
	f         io.ReadSeekCloser
	br        *bufio.Reader
	fmt       Format
	inBits    int
	nSamples  uint64
	dataStart int64
	size      int64
	shift     int // input bits -> output bits
	pend      []byte
	skip      int // frames to drop after a seek
}

func openFLAC(path string, open opener) (Source, error) {
	f, err := open(path)
	if err != nil {
		return nil, err
	}
	s := &flacSource{path: path, f: f}
	if err := s.readMeta(); err != nil {
		f.Close()
		return nil, err
	}
	out := 16
	if s.inBits > 16 {
		out = 24
	}
	s.fmt.Bits, s.shift = out, out-s.inBits
	if s.fmt.Channels > 2 {
		s.fmt.Channels = 2 // keep front L/R
	}
	if s.size, err = f.Seek(0, io.SeekEnd); err != nil {
		f.Close()
		return nil, err
	}
	if _, err := f.Seek(s.dataStart, io.SeekStart); err != nil {
		f.Close()
		return nil, err
	}
	s.br = bufio.NewReaderSize(f, 256<<10)
	return s, nil
}

// readMeta parses "fLaC" + metadata blocks: STREAMINFO for the format, the rest is skipped.
func (s *flacSource) readMeta() error {
	var h [4]byte
	if _, err := io.ReadFull(s.f, h[:]); err != nil || string(h[:]) != "fLaC" {
		return errors.New("not a FLAC file")
	}
	pos := int64(4)
	for {
		if _, err := io.ReadFull(s.f, h[:]); err != nil {
			return err
		}
		last, typ, n := h[0]&0x80 != 0, h[0]&0x7F, int64(h[1])<<16|int64(h[2])<<8|int64(h[3])
		pos += 4
		if typ == 0 { // STREAMINFO
			b := make([]byte, n)
			if _, err := io.ReadFull(s.f, b); err != nil || n < 18 {
				return errors.New("FLAC: bad STREAMINFO")
			}
			x := binary.BigEndian.Uint64(b[10:18])
			s.fmt.Rate = int(x >> 44)
			s.fmt.Channels = int(x>>41&7) + 1
			s.inBits = int(x>>36&31) + 1
			s.nSamples = x & (1<<36 - 1)
		} else if _, err := s.f.Seek(n, io.SeekCurrent); err != nil {
			return err
		}
		pos += n
		if last {
			break
		}
	}
	s.dataStart = pos
	if s.fmt.Rate == 0 {
		return errors.New("FLAC: no STREAMINFO")
	}
	return nil
}

func (s *flacSource) Format() Format { return s.fmt }

func (s *flacSource) Duration() float64 {
	return float64(s.nSamples) / float64(s.fmt.Rate)
}

func (s *flacSource) Read(p []byte) (int, error) {
	fs := s.fmt.FrameSize()
	for len(s.pend) == 0 {
		fr, err := frame.Parse(s.br)
		if err != nil {
			if err == io.ErrUnexpectedEOF {
				err = io.EOF
			}
			return 0, err
		}
		s.pend = s.decode(fr)
	}
	n := copy(p[:len(p)/fs*fs], s.pend)
	s.pend = s.pend[n:]
	return n, nil
}

// decode turns a parsed frame into output PCM, dropping s.skip leading samples.
func (s *flacSource) decode(fr *frame.Frame) []byte {
	n := len(fr.Subframes[0].Samples)
	buf := make([]byte, 0, n*s.fmt.FrameSize())
	for i := s.skip; i < n; i++ {
		for ch := 0; ch < s.fmt.Channels; ch++ {
			v := fr.Subframes[ch].Samples[i]
			if s.shift > 0 {
				v <<= uint(s.shift)
			} else if s.shift < 0 {
				v >>= uint(-s.shift)
			}
			buf = appendSample(buf, v, s.fmt.Bits)
		}
	}
	s.skip = max(0, s.skip-n)
	return buf
}

// probe finds the first valid frame at or after off: its file offset and first sample number.
func probeFLAC(r io.ReaderAt, off, size int64) (int64, uint64, bool) {
	const window = 64 << 10
	b := make([]byte, window)
	n, _ := r.ReadAt(b, off)
	for i := 0; i+1 < n; i++ {
		if b[i] != 0xFF || b[i+1]&0xFE != 0xF8 {
			continue
		}
		at := off + int64(i)
		fr, err := frame.Parse(bufio.NewReaderSize(io.NewSectionReader(r, at, size-at), 32<<10))
		if err == nil {
			return at, fr.SampleNumber(), true
		}
	}
	return 0, 0, false
}

func (s *flacSource) Seek(sec float64) error {
	target := uint64(sec * float64(s.fmt.Rate))
	pos := s.dataStart
	if target > 0 {
		pf, err := os.Open(s.path) // probes use their own handle: the main reader keeps its read-ahead
		if err != nil {
			return err
		}
		lo, hi := s.dataStart, s.size
		for hi-lo > 256<<10 {
			mid := lo + (hi-lo)/2
			at, num, ok := probeFLAC(pf, mid, s.size)
			if !ok || at >= hi {
				hi = mid
				continue
			}
			if num <= target {
				lo = at
			} else {
				hi = mid
			}
		}
		pf.Close()
		pos = lo
	}
	if _, err := s.f.Seek(pos, io.SeekStart); err != nil {
		return err
	}
	s.br.Reset(s.f)
	s.pend, s.skip = nil, 0
	for { // walk the last few frames up to the one that holds the target sample
		fr, err := frame.Parse(s.br)
		if err != nil {
			return err
		}
		first := fr.SampleNumber()
		if first+uint64(len(fr.Subframes[0].Samples)) > target {
			if target > first {
				s.skip = int(target - first)
			}
			s.pend = s.decode(fr)
			return nil
		}
	}
}

func (s *flacSource) Close() error { return s.f.Close() }

func appendSample(b []byte, v int32, bits int) []byte {
	if bits == 16 {
		return append(b, byte(v), byte(v>>8))
	}
	return append(b, byte(v), byte(v>>8), byte(v>>16))
}

// ---------- WAV (PCM 16/24) ----------

type wavSource struct {
	f          io.ReadSeekCloser
	fmt        Format
	dataOff    int64
	dataLen    int64
	pos        int64
	inBits     int
	r          *bufio.Reader
	frameBytes int
}

func openWAV(path string, open opener) (Source, error) {
	f, err := open(path)
	if err != nil {
		return nil, err
	}
	var hdr [12]byte
	if _, err := io.ReadFull(f, hdr[:]); err != nil || string(hdr[0:4]) != "RIFF" || string(hdr[8:12]) != "WAVE" {
		f.Close()
		return nil, errors.New("not a WAV file")
	}
	s := &wavSource{f: f}
	for {
		var ch [8]byte
		if _, err := io.ReadFull(f, ch[:]); err != nil {
			f.Close()
			return nil, errors.New("WAV: no data chunk")
		}
		size := int64(binary.LittleEndian.Uint32(ch[4:]))
		switch string(ch[0:4]) {
		case "fmt ":
			b := make([]byte, size)
			if _, err := io.ReadFull(f, b); err != nil || len(b) < 16 {
				f.Close()
				return nil, errors.New("WAV: bad fmt chunk")
			}
			tag := binary.LittleEndian.Uint16(b[0:])
			s.fmt.Channels = int(binary.LittleEndian.Uint16(b[2:]))
			s.fmt.Rate = int(binary.LittleEndian.Uint32(b[4:]))
			s.inBits = int(binary.LittleEndian.Uint16(b[14:]))
			if tag != 1 && tag != 0xFFFE || (s.inBits != 16 && s.inBits != 24) || s.fmt.Channels < 1 || s.fmt.Channels > 2 {
				f.Close()
				return nil, errors.New("WAV: only PCM 16/24 bit, mono/stereo")
			}
			s.fmt.Bits = s.inBits
			if size%2 == 1 {
				f.Seek(1, io.SeekCurrent)
			}
		case "data":
			s.dataOff, _ = f.Seek(0, io.SeekCurrent)
			s.dataLen = size
			peek := make([]byte, 64<<10)
			n, _ := io.ReadFull(f, peek)
			if looksCompressed(peek[:n]) { // a "DTS-WAV": compressed surround, not PCM
				f.Close()
				return nil, errCompressedWAV
			}
			if _, err := f.Seek(s.dataOff, io.SeekStart); err != nil {
				f.Close()
				return nil, err
			}
			s.frameBytes = s.fmt.FrameSize()
			if s.frameBytes == 0 {
				f.Close()
				return nil, errors.New("WAV: data before fmt")
			}
			s.r = bufio.NewReaderSize(f, 64<<10)
			return s, nil
		default:
			f.Seek(size+size%2, io.SeekCurrent)
		}
	}
}

func (s *wavSource) Format() Format { return s.fmt }
func (s *wavSource) Duration() float64 {
	return float64(s.dataLen) / float64(s.frameBytes) / float64(s.fmt.Rate)
}

func (s *wavSource) Read(p []byte) (int, error) {
	left := s.dataLen - s.pos
	if left <= 0 {
		return 0, io.EOF
	}
	n := len(p) / s.frameBytes * s.frameBytes
	if int64(n) > left {
		n = int(left / int64(s.frameBytes) * int64(s.frameBytes))
	}
	n, err := io.ReadFull(s.r, p[:n])
	n = n / s.frameBytes * s.frameBytes
	s.pos += int64(n)
	if err == io.ErrUnexpectedEOF {
		err = io.EOF
	}
	return n, err
}

func (s *wavSource) Seek(sec float64) error {
	off := int64(sec*float64(s.fmt.Rate)) * int64(s.frameBytes)
	if off > s.dataLen {
		off = s.dataLen
	}
	if _, err := s.f.Seek(s.dataOff+off, io.SeekStart); err != nil {
		return err
	}
	s.pos = off
	s.r.Reset(s.f)
	return nil
}

func (s *wavSource) Close() error { return s.f.Close() }

// ---------- MP3 file ----------

type mp3Source struct {
	f   io.ReadSeekCloser
	d   *mp3.Decoder
	fmt Format
}

func openMP3File(path string, open opener) (Source, error) {
	f, err := open(path)
	if err != nil {
		return nil, err
	}
	d, err := mp3.NewDecoder(f)
	if err != nil {
		f.Close()
		return nil, err
	}
	return &mp3Source{f: f, d: d, fmt: Format{Rate: d.SampleRate(), Bits: 16, Channels: 2}}, nil
}

func (s *mp3Source) Format() Format { return s.fmt }
func (s *mp3Source) Duration() float64 {
	if l := s.d.Length(); l > 0 {
		return float64(l) / 4 / float64(s.fmt.Rate)
	}
	return 0
}
func (s *mp3Source) Read(p []byte) (int, error) {
	n, err := s.d.Read(p[:len(p)/4*4])
	return n / 4 * 4, err
}
func (s *mp3Source) Seek(sec float64) error {
	_, err := s.d.Seek(int64(sec*float64(s.fmt.Rate))*4, io.SeekStart)
	return err
}
func (s *mp3Source) Close() error { return s.f.Close() }

// ---------- MP3 internet radio ----------

type radioSource struct {
	resp *http.Response
	d    *mp3.Decoder
	fmt  Format
	meta *icyReader
}

var radioClient = &http.Client{Timeout: 0, Transport: &http.Transport{ResponseHeaderTimeout: 10 * time.Second}}

func openRadio(url string, onTitle func(string)) (Source, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Icy-MetaData", "1")
	req.Header.Set("User-Agent", "S3HiFi/0.1")
	resp, err := radioClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		resp.Body.Close()
		return nil, fmt.Errorf("radio: HTTP %d", resp.StatusCode)
	}
	ct := strings.ToLower(resp.Header.Get("Content-Type"))
	if strings.Contains(ct, "aac") || strings.Contains(ct, "ogg") {
		resp.Body.Close()
		return nil, fmt.Errorf("формат %s пока не поддерживается, нужна станция в MP3", ct)
	}
	var body io.Reader = bufio.NewReaderSize(resp.Body, 64<<10)
	ir := &icyReader{r: body, onTitle: onTitle}
	if mi, _ := strconv.Atoi(resp.Header.Get("icy-metaint")); mi > 0 {
		ir.metaint = mi
		ir.left = mi
	}
	d, err := mp3.NewDecoder(ir)
	if err != nil {
		resp.Body.Close()
		return nil, err
	}
	if n := resp.Header.Get("icy-name"); n != "" && onTitle != nil {
		onTitle("")
	}
	return &radioSource{resp: resp, d: d, fmt: Format{Rate: d.SampleRate(), Bits: 16, Channels: 2}, meta: ir}, nil
}

func (s *radioSource) Format() Format    { return s.fmt }
func (s *radioSource) Duration() float64 { return 0 }
func (s *radioSource) Read(p []byte) (int, error) {
	n, err := s.d.Read(p[:len(p)/4*4])
	return n / 4 * 4, err
}
func (s *radioSource) Seek(float64) error { return ErrNoSeek }
func (s *radioSource) Close() error       { return s.resp.Body.Close() }

// icyReader strips SHOUTcast metadata blocks and reports StreamTitle.
type icyReader struct {
	r       io.Reader
	metaint int
	left    int
	onTitle func(string)
	mu      sync.Mutex
}

func (ir *icyReader) Read(p []byte) (int, error) {
	if ir.metaint == 0 {
		return ir.r.Read(p)
	}
	if ir.left == 0 {
		var l [1]byte
		if _, err := io.ReadFull(ir.r, l[:]); err != nil {
			return 0, err
		}
		if n := int(l[0]) * 16; n > 0 {
			meta := make([]byte, n)
			if _, err := io.ReadFull(ir.r, meta); err != nil {
				return 0, err
			}
			if t := parseStreamTitle(string(meta)); t != "" && ir.onTitle != nil {
				ir.onTitle(t)
			}
		}
		ir.left = ir.metaint
	}
	if len(p) > ir.left {
		p = p[:ir.left]
	}
	n, err := ir.r.Read(p)
	ir.left -= n
	return n, err
}

func parseStreamTitle(m string) string {
	i := strings.Index(m, "StreamTitle='")
	if i < 0 {
		return ""
	}
	m = m[i+len("StreamTitle='"):]
	if j := strings.Index(m, "';"); j >= 0 {
		m = m[:j]
	}
	return strings.TrimSpace(strings.TrimRight(m, "\x00"))
}
