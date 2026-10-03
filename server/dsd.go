package main

// DSD (DSF / DSDIFF) -> PCM 24 bit. DSD64 and DSD128/256 are decimated to 88.2 kHz
// (96 kHz for the 48k family) with a linear-phase Kaiser-windowed FIR and byte lookup tables.

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"sync"
)

const dsdSilence = 0x69 // balanced idle pattern, keeps the filter free of DC

type dsdSource struct {
	f        io.ReadSeekCloser
	fmt      Format
	dsdRate  int
	ratio    int // DSD bits per PCM sample
	totalB   int64
	posB     int64 // bytes consumed per channel
	readCh   func(n int, out [][]byte) (int, error)
	seekB    func(b int64) error
	filt     *dsdFilter
	hist     [][]byte // per channel: the last m-1 bytes, oldest first
	work     [][]byte // per channel scratch: history + new bytes
	chBuf    [][]byte
	inCh     int
	msbFirst bool
}

func openDSD(path string, open opener) (Source, error) {
	f, err := open(path)
	if err != nil {
		return nil, err
	}
	var magic [4]byte
	if _, err := io.ReadFull(f, magic[:]); err != nil {
		f.Close()
		return nil, err
	}
	f.Seek(0, io.SeekStart)
	var s *dsdSource
	switch string(magic[:]) {
	case "DSD ":
		s, err = openDSF(f)
	case "FRM8":
		s, err = openDFF(f)
	default:
		err = errors.New("not a DSD file")
	}
	if err != nil {
		f.Close()
		return nil, err
	}
	return s.finish()
}

// finish sets up the decimation filter once the DSD rate and channel count are known.
func (s *dsdSource) finish() (Source, error) {
	base := 88200
	if s.dsdRate%88200 != 0 {
		base = 96000
	}
	s.ratio = s.dsdRate / base
	if s.dsdRate%base != 0 || s.ratio < 8 || s.ratio%8 != 0 {
		s.f.Close()
		return nil, fmt.Errorf("DSD: unsupported rate %d", s.dsdRate)
	}
	out := s.inCh
	if out > 2 {
		out = 2
	}
	s.fmt = Format{Rate: base, Bits: 24, Channels: out}
	s.filt = getDSDFilter(s.ratio)
	s.chBuf = make([][]byte, s.inCh)
	s.resetHistory()
	return s, nil
}

func (s *dsdSource) resetHistory() {
	m := len(s.filt.table)
	s.hist = make([][]byte, s.inCh)
	s.work = make([][]byte, s.inCh)
	for c := range s.hist {
		s.hist[c] = make([]byte, m-1)
		for i := range s.hist[c] {
			s.hist[c][i] = dsdSilence
		}
	}
}

func (s *dsdSource) Format() Format { return s.fmt }
func (s *dsdSource) Duration() float64 {
	return float64(s.totalB*8) / float64(s.dsdRate)
}
func (s *dsdSource) Close() error { return s.f.Close() }

func (s *dsdSource) Seek(sec float64) error {
	b := int64(sec*float64(s.dsdRate)) / 8
	b -= b % int64(s.ratio/8)
	if b > s.totalB {
		b = s.totalB
	}
	if err := s.seekB(b); err != nil {
		return err
	}
	s.posB = b
	s.resetHistory()
	return nil
}

func (s *dsdSource) Read(p []byte) (int, error) {
	frames := len(p) / s.fmt.FrameSize()
	step := s.ratio / 8 // bytes per channel per PCM sample
	need := int64(frames * step)
	if left := s.totalB - s.posB; need > left {
		need = left - left%int64(step)
		frames = int(need) / step
	}
	if frames == 0 {
		return 0, io.EOF
	}
	for c := range s.chBuf {
		if cap(s.chBuf[c]) < int(need) {
			s.chBuf[c] = make([]byte, need)
		}
		s.chBuf[c] = s.chBuf[c][:need]
	}
	got, err := s.readCh(int(need), s.chBuf)
	frames = got / step
	if frames == 0 {
		if err == nil {
			err = io.EOF
		}
		return 0, err
	}
	s.posB += int64(frames * step)
	var wg sync.WaitGroup
	for c := 0; c < s.fmt.Channels; c++ { // channels are independent: one core each
		wg.Add(1)
		go func(c int) {
			defer wg.Done()
			s.filterChannel(c, s.chBuf[c][:frames*step], frames, p)
		}(c)
	}
	wg.Wait()
	return frames * s.fmt.FrameSize(), nil
}

// filterChannel decimates one channel. hist[c] holds the last m-1 bytes, oldest first;
// it is joined with the new bytes so the FIR window is always a plain slice.
func (s *dsdSource) filterChannel(c int, in []byte, frames int, p []byte) {
	tab := s.filt.table
	m := len(tab)
	step := s.ratio / 8
	x := append(s.work[c][:0], s.hist[c]...)
	for _, b := range in {
		if s.msbFirst {
			b = bitRev[b]
		}
		x = append(x, b)
	}
	s.work[c] = x
	ch := s.fmt.Channels
	for i := 0; i < frames; i++ {
		newest := m - 2 + (i+1)*step // index of the newest byte for this sample
		w := x[newest-m+1 : newest+1]
		var acc float32
		for k := 0; k < m; k++ {
			acc += tab[k][w[m-1-k]]
		}
		v := int32(math.Round(float64(acc) * 8388607))
		if v > 8388607 {
			v = 8388607
		} else if v < -8388608 {
			v = -8388608
		}
		o := (i*ch + c) * 3
		p[o], p[o+1], p[o+2] = byte(v), byte(v>>8), byte(v>>16)
	}
	s.hist[c] = append(s.hist[c][:0], x[len(x)-(m-1):]...)
}

// ---------- DSF ----------

func openDSF(f io.ReadSeekCloser) (*dsdSource, error) {
	var h [28 + 52 + 12]byte
	if _, err := io.ReadFull(f, h[:]); err != nil {
		return nil, err
	}
	le := binary.LittleEndian
	if string(h[28:32]) != "fmt " || string(h[80:84]) != "data" {
		return nil, errors.New("DSF: bad header")
	}
	fm := h[40:]
	if le.Uint32(fm[4:]) != 0 {
		return nil, errors.New("DSF: compressed data is not supported")
	}
	ch := int(le.Uint32(fm[12:]))
	rate := int(le.Uint32(fm[16:]))
	bps := le.Uint32(fm[20:])
	samples := int64(le.Uint64(fm[24:]))
	block := int(le.Uint32(fm[32:]))
	if ch < 1 || block <= 0 {
		return nil, errors.New("DSF: bad format")
	}
	dataOff := int64(92)
	s := &dsdSource{f: f, dsdRate: rate, inCh: ch, totalB: (samples + 7) / 8, msbFirst: bps == 8}
	group := make([]byte, block*ch)
	gpos := block // position inside the current block (per channel); block = empty
	r := bufio.NewReaderSize(f, len(group)*2)
	s.readCh = func(n int, out [][]byte) (int, error) {
		done := 0
		for done < n {
			if gpos == block {
				if _, err := io.ReadFull(r, group); err != nil {
					return done, err
				}
				gpos = 0
			}
			k := block - gpos
			if k > n-done {
				k = n - done
			}
			for c := 0; c < ch; c++ {
				copy(out[c][done:done+k], group[c*block+gpos:c*block+gpos+k])
			}
			gpos += k
			done += k
		}
		return done, nil
	}
	s.seekB = func(b int64) error {
		g := b / int64(block)
		if _, err := f.Seek(dataOff+g*int64(block*ch), io.SeekStart); err != nil {
			return err
		}
		r.Reset(f)
		gpos = block
		if skip := int(b % int64(block)); skip > 0 {
			if _, err := io.ReadFull(r, group); err != nil {
				return err
			}
			gpos = skip
		}
		return nil
	}
	return s, nil
}

// ---------- DSDIFF (.dff) ----------

func openDFF(f io.ReadSeekCloser) (*dsdSource, error) {
	be := binary.BigEndian
	var hdr [16]byte
	if _, err := io.ReadFull(f, hdr[:]); err != nil || string(hdr[12:16]) != "DSD " {
		return nil, errors.New("DFF: bad header")
	}
	s := &dsdSource{f: f, msbFirst: true}
	var dataOff int64
	for dataOff == 0 {
		var ch [12]byte
		if _, err := io.ReadFull(f, ch[:]); err != nil {
			return nil, errors.New("DFF: no DSD data")
		}
		size := int64(be.Uint64(ch[4:]))
		id := string(ch[0:4])
		switch id {
		case "PROP":
			var st [4]byte
			io.ReadFull(f, st[:])
			end, _ := f.Seek(0, io.SeekCurrent)
			end += size - 4
			for {
				cur, _ := f.Seek(0, io.SeekCurrent)
				if cur >= end {
					break
				}
				var sc [12]byte
				if _, err := io.ReadFull(f, sc[:]); err != nil {
					return nil, err
				}
				ss := int64(be.Uint64(sc[4:]))
				b := make([]byte, ss)
				io.ReadFull(f, b)
				switch string(sc[0:4]) {
				case "FS  ":
					s.dsdRate = int(be.Uint32(b))
				case "CHNL":
					s.inCh = int(be.Uint16(b))
				case "CMPR":
					if string(b[0:4]) != "DSD " {
						return nil, errors.New("DFF: DST compression is not supported")
					}
				}
				if ss%2 == 1 {
					f.Seek(1, io.SeekCurrent)
				}
			}
		case "DSD ":
			dataOff, _ = f.Seek(0, io.SeekCurrent)
			if s.inCh < 1 || s.dsdRate == 0 {
				return nil, errors.New("DFF: missing properties")
			}
			s.totalB = size / int64(s.inCh)
		case "DST ":
			return nil, errors.New("DFF: DST compression is not supported")
		default:
			f.Seek(size+size%2, io.SeekCurrent)
		}
	}
	r := bufio.NewReaderSize(f, 128<<10)
	inter := make([]byte, 0)
	s.readCh = func(n int, out [][]byte) (int, error) {
		need := n * s.inCh
		if cap(inter) < need {
			inter = make([]byte, need)
		}
		inter = inter[:need]
		got, err := io.ReadFull(r, inter)
		k := got / s.inCh
		for i := 0; i < k; i++ {
			for c := 0; c < s.inCh; c++ {
				out[c][i] = inter[i*s.inCh+c]
			}
		}
		if err == io.ErrUnexpectedEOF {
			err = io.EOF
		}
		return k, err
	}
	s.seekB = func(b int64) error {
		if _, err := f.Seek(dataOff+b*int64(s.inCh), io.SeekStart); err != nil {
			return err
		}
		r.Reset(f)
		return nil
	}
	return s, nil
}

// ---------- filter ----------

type dsdFilter struct {
	table [][256]float32 // [byte index from newest][byte value] -> partial sum
}

var (
	dsdFilters   = map[int]*dsdFilter{}
	dsdFiltersMu sync.Mutex
	bitRev       [256]byte
)

func init() {
	for i := 0; i < 256; i++ {
		var r byte
		for b := 0; b < 8; b++ {
			if i&(1<<b) != 0 {
				r |= 1 << (7 - b)
			}
		}
		bitRev[i] = r
	}
}

func getDSDFilter(ratio int) *dsdFilter {
	dsdFiltersMu.Lock()
	defer dsdFiltersMu.Unlock()
	if f, ok := dsdFilters[ratio]; ok {
		return f
	}
	n := 32 * ratio // taps: 1024 for DSD64
	fc := 30000.0 / (88200.0 * float64(ratio))
	beta := 9.0
	h := make([]float64, n)
	var sum float64
	for i := 0; i < n; i++ {
		x := float64(i) - float64(n-1)/2
		sinc := 2 * fc
		if x != 0 {
			sinc = math.Sin(2*math.Pi*fc*x) / (math.Pi * x)
		}
		r := 2*float64(i)/float64(n-1) - 1
		w := bessI0(beta*math.Sqrt(1-r*r)) / bessI0(beta)
		h[i] = sinc * w
		sum += h[i]
	}
	for i := range h {
		h[i] /= sum
	}
	m := n / 8
	f := &dsdFilter{table: make([][256]float32, m)}
	for k := 0; k < m; k++ {
		for b := 0; b < 256; b++ {
			var acc float64
			for bit := 0; bit < 8; bit++ { // bit 0 is the oldest in time (LSB first)
				v := -1.0
				if b&(1<<bit) != 0 {
					v = 1
				}
				acc += h[8*k+7-bit] * v
			}
			f.table[k][b] = float32(acc)
		}
	}
	dsdFilters[ratio] = f
	return f
}

func bessI0(x float64) float64 {
	sum, term := 1.0, 1.0
	for k := 1; k < 50; k++ {
		term *= (x / (2 * float64(k))) * (x / (2 * float64(k)))
		sum += term
		if term < 1e-12*sum {
			break
		}
	}
	return sum
}
