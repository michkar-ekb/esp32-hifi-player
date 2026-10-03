package main

// ffmpeg as an external decoder for what the server does not decode itself:
// DTS / AC-3 (e.g. "DTS-WAV" discs, downmixed to stereo), ALAC / AAC (.m4a), OGG, Opus,
// APE, WavPack, AIFF, WMA and any file the built-in decoders reject.
// ffmpeg runs as a separate process (an LGPL build is enough), found in PATH or next to the server.

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
)

var ffmpegExt = map[string]bool{
	".m4a": true, ".alac": true, ".aac": true, ".ogg": true, ".oga": true, ".opus": true,
	".ape": true, ".wv": true, ".aiff": true, ".aif": true, ".wma": true, ".dts": true, ".ac3": true,
}

var (
	ffOnce                sync.Once
	ffmpegBin, ffprobeBin string
)

func findFF() {
	ffOnce.Do(func() {
		exe := ""
		if runtime.GOOS == "windows" {
			exe = ".exe"
		}
		dir := ""
		if self, err := os.Executable(); err == nil {
			dir = filepath.Dir(self)
		}
		find := func(name string) string {
			if dir != "" {
				if p := filepath.Join(dir, name+exe); fileExists(p) {
					return p
				}
			}
			if p, err := exec.LookPath(name); err == nil {
				return p
			}
			return ""
		}
		ffmpegBin, ffprobeBin = find("ffmpeg"), find("ffprobe")
	})
}

func fileExists(p string) bool { st, err := os.Stat(p); return err == nil && !st.IsDir() }

func haveFFmpeg() bool { findFF(); return ffmpegBin != "" && ffprobeBin != "" }

var errNoFFmpeg = errors.New("для этого формата нужен ffmpeg")

type ffProbe struct {
	Streams []struct {
		SampleRate string `json:"sample_rate"`
		Channels   int    `json:"channels"`
		SampleFmt  string `json:"sample_fmt"`
		Codec      string `json:"codec_name"`
	} `json:"streams"`
	Format struct {
		Duration string `json:"duration"`
	} `json:"format"`
}

func probe(path string) (*ffProbe, error) {
	if !haveFFmpeg() {
		return nil, errNoFFmpeg
	}
	out, err := hiddenCmd(ffprobeBin, "-v", "error", "-select_streams", "a:0",
		"-show_entries", "stream=sample_rate,channels,sample_fmt,codec_name:format=duration", "-of", "json", path).Output()
	if err != nil {
		return nil, fmt.Errorf("ffprobe: %v", err)
	}
	var p ffProbe
	if err := json.Unmarshal(out, &p); err != nil || len(p.Streams) == 0 {
		return nil, errors.New("ffprobe: no audio stream")
	}
	return &p, nil
}

type ffmpegSource struct {
	path    string
	fmt     Format
	dur     float64
	cmd     *exec.Cmd
	out     io.ReadCloser
	br      *bufio.Reader
	codec   string
	srcRate int
	skip    int      // output frames to drop after a seek that started at a block boundary
	stdin   *os.File // WavPack after a seek: ffmpeg reads the file from this handle
}

// maxRate: above this the server resamples (Wi-Fi to the player carries ~5–8 Mbit/s;
// 24/96 is 4.6 Mbit/s, 24/192 would be 9.2).
const maxRate = 96000

func outRate(rate int) int {
	if rate <= maxRate {
		return rate
	}
	if rate%44100 == 0 {
		return 88200
	}
	return 96000
}

func openFFmpeg(path string) (Source, error) {
	pr, err := probe(path)
	if err != nil {
		return nil, err
	}
	st := pr.Streams[0]
	rate, _ := strconv.Atoi(st.SampleRate)
	if rate <= 0 {
		return nil, errors.New("ffprobe: unknown sample rate")
	}
	bits := 24
	switch st.SampleFmt {
	case "s16", "s16p", "u8", "u8p":
		bits = 16
	}
	s := &ffmpegSource{path: path, fmt: Format{Rate: outRate(rate), SrcRate: rate, Bits: bits, Channels: 2}, codec: st.Codec, srcRate: rate}
	if st.Channels > 2 || st.Codec == "dts" || st.Codec == "ac3" || st.Codec == "eac3" {
		s.fmt.Codec = st.Codec // shown in the remote: "DTS → стерео"
	}
	s.dur, _ = strconv.ParseFloat(pr.Format.Duration, 64)
	return s, s.start(0)
}

func (s *ffmpegSource) start(pos float64) error {
	s.stop()
	s.skip = 0
	args := []string{"-v", "error"}
	input := []string{"-i", s.path}
	var stdin *os.File
	if pos > 0 && strings.EqualFold(filepath.Ext(s.path), ".wv") {
		// ffmpeg would read a WavPack file from the start to seek (77 s for 5 min over a share):
		// find the block ourselves and let ffmpeg read from there
		if off, first, ok := seekWavPack(s.path, uint64(pos*float64(s.srcRate))); ok {
			f, err := os.Open(s.path)
			if err == nil {
				if _, err = f.Seek(off, io.SeekStart); err == nil {
					stdin = f
					input = []string{"-f", "wv", "-i", "pipe:0"}
					s.skip = int((pos*float64(s.srcRate) - float64(first)) * float64(s.fmt.Rate) / float64(s.srcRate))
				} else {
					f.Close()
				}
			}
		}
	}
	if stdin == nil {
		args = append(args, "-nostdin")
		if pos > 0 {
			args = append(args, "-ss", strconv.FormatFloat(pos, 'f', 3, 64))
		}
	}
	sf := "s24le"
	if s.fmt.Bits == 16 {
		sf = "s16le"
	}
	args = append(args, input...)
	args = append(args, "-map", "0:a:0", "-vn", "-ac", "2")
	if s.fmt.Rate != s.srcRate { // high-quality downsampling (SoX resampler)
		args = append(args, "-af", "aresample=resampler=soxr:precision=28", "-ar", strconv.Itoa(s.fmt.Rate))
	}
	args = append(args, "-f", sf, "-")
	cmd := hiddenCmd(ffmpegBin, args...)
	if stdin != nil {
		cmd.Stdin = stdin
	}
	s.stdin = stdin
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	s.cmd, s.out, s.br = cmd, out, bufio.NewReaderSize(out, 256<<10)
	return nil
}

func (s *ffmpegSource) stop() {
	if s.cmd != nil {
		s.out.Close()
		s.cmd.Process.Kill()
		s.cmd.Wait()
		s.cmd = nil
	}
	if s.stdin != nil {
		s.stdin.Close()
		s.stdin = nil
	}
}

func (s *ffmpegSource) Format() Format    { return s.fmt }
func (s *ffmpegSource) Duration() float64 { return s.dur }
func (s *ffmpegSource) Seek(sec float64) error {
	return s.start(sec)
}
func (s *ffmpegSource) Close() error { s.stop(); return nil }

func (s *ffmpegSource) Read(p []byte) (int, error) {
	fs := s.fmt.FrameSize()
	for s.skip > 0 { // drop the start of the block we began at
		n := min(s.skip*fs, len(p)/fs*fs)
		k, err := io.ReadFull(s.br, p[:n])
		s.skip -= k / fs
		if err != nil {
			return 0, err
		}
	}
	n, err := io.ReadFull(s.br, p[:len(p)/fs*fs])
	n = n / fs * fs
	if err == io.ErrUnexpectedEOF {
		err = io.EOF
	}
	return n, err
}

func ffDuration(path string) float64 {
	pr, err := probe(path)
	if err != nil {
		return 0
	}
	d, _ := strconv.ParseFloat(pr.Format.Duration, 64)
	return d
}

// ---------- DTS / AC-3 hidden in a WAV ----------

var errCompressedWAV = errors.New("в WAV лежит DTS или AC-3, а не обычный звук")

// looksCompressed finds DTS or AC-3 sync words in the first bytes of WAV data.
// Plain PCM practically never contains them twice in a row of 64 KB.
func looksCompressed(b []byte) bool {
	pats := [][]byte{
		{0xFE, 0x7F, 0x01, 0x80}, {0x7F, 0xFE, 0x80, 0x01}, // DTS 16-bit, LE / BE
		{0xFF, 0x1F, 0x00, 0xE8}, {0x1F, 0xFF, 0xE8, 0x00}, // DTS 14-bit, LE / BE
		{0x72, 0xF8, 0x1F, 0x4E}, // IEC 61937 burst preamble (AC-3 / DTS in S/PDIF framing)
	}
	for _, pat := range pats {
		hits := 0
		for i := 0; i+4 <= len(b); i += 2 {
			if b[i] == pat[0] && b[i+1] == pat[1] && b[i+2] == pat[2] && b[i+3] == pat[3] {
				if hits++; hits >= 2 {
					return true
				}
			}
		}
	}
	return false
}

// seekWavPack bisects a WavPack file for the block that holds sample `target`:
// every block starts with "wvpk" and carries the index of its first sample.
func seekWavPack(path string, target uint64) (int64, uint64, bool) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, false
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return 0, 0, false
	}
	probe := func(off int64) (int64, uint64, bool) { // first initial block at or after off
		b := make([]byte, 128<<10)
		n, _ := f.ReadAt(b, off)
		for i := 0; i+32 <= n; i++ {
			if b[i] != 'w' || string(b[i:i+4]) != "wvpk" {
				continue
			}
			le := binary.LittleEndian
			ver, size := le.Uint16(b[i+8:]), le.Uint32(b[i+4:])
			flags := le.Uint32(b[i+24:])
			if ver < 0x402 || ver > 0x410 || size < 24 || size > 1<<24 || le.Uint32(b[i+20:]) == 0 || flags&0x800 == 0 {
				continue // not a real block header, or not the first block of a sample range
			}
			idx := uint64(b[i+10])<<32 | uint64(le.Uint32(b[i+16:]))
			return off + int64(i), idx, true
		}
		return 0, 0, false
	}
	lo, loIdx, ok := probe(0)
	if !ok {
		return 0, 0, false
	}
	hi := st.Size()
	for hi-lo > 512<<10 {
		mid := lo + (hi-lo)/2
		at, idx, ok := probe(mid)
		if !ok || at >= hi {
			hi = mid
			continue
		}
		if idx <= target {
			lo, loIdx = at, idx
		} else {
			hi = mid
		}
	}
	// walk forward block by block to the last one that starts at or before the target
	for {
		at, idx, ok := probe(lo + 1)
		if !ok || idx > target || at >= st.Size() {
			return lo, loIdx, true
		}
		lo, loIdx = at, idx
	}
}
