package main

// ffmpeg as an external decoder for what the server does not decode itself:
// DTS / AC-3 (e.g. "DTS-WAV" discs, downmixed to stereo), ALAC / AAC (.m4a), OGG, Opus,
// APE, WavPack, AIFF, WMA and any file the built-in decoders reject.
// ffmpeg runs as a separate process (an LGPL build is enough), found in PATH or next to the server.

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
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
	path  string
	fmt   Format
	dur   float64
	cmd   *exec.Cmd
	out   io.ReadCloser
	br    *bufio.Reader
	codec string
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
	s := &ffmpegSource{path: path, fmt: Format{Rate: rate, Bits: bits, Channels: 2}, codec: st.Codec}
	if st.Channels > 2 || st.Codec == "dts" || st.Codec == "ac3" || st.Codec == "eac3" {
		s.fmt.Codec = st.Codec // shown in the remote: "DTS → стерео"
	}
	s.dur, _ = strconv.ParseFloat(pr.Format.Duration, 64)
	return s, s.start(0)
}

func (s *ffmpegSource) start(pos float64) error {
	s.stop()
	args := []string{"-nostdin", "-v", "error"}
	if pos > 0 {
		args = append(args, "-ss", strconv.FormatFloat(pos, 'f', 3, 64))
	}
	sf := "s24le"
	if s.fmt.Bits == 16 {
		sf = "s16le"
	}
	args = append(args, "-i", s.path, "-map", "0:a:0", "-vn", "-ac", "2", "-f", sf, "-")
	cmd := hiddenCmd(ffmpegBin, args...)
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
}

func (s *ffmpegSource) Format() Format    { return s.fmt }
func (s *ffmpegSource) Duration() float64 { return s.dur }
func (s *ffmpegSource) Seek(sec float64) error {
	return s.start(sec)
}
func (s *ffmpegSource) Close() error { s.stop(); return nil }

func (s *ffmpegSource) Read(p []byte) (int, error) {
	fs := s.fmt.FrameSize()
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
