package main

import (
	"errors"
	"io"
	"os"
	"sync"
)

// prefetchFile reads a file ahead into memory in a background goroutine.
// Music often lives on a network share (SMB, sshfs) that can stall for seconds;
// with 32 MB read ahead (~20 s of DSD128, minutes of FLAC) such stalls are not heard.
type prefetchFile struct {
	f    *os.File
	size int64

	mu      sync.Mutex
	cond    *sync.Cond
	blocks  [][]byte // data ready to read, in order
	queued  int64    // bytes in blocks
	readPos int64    // file offset of the next byte Read returns
	nextPos int64    // file offset the fetcher reads next
	gen     int      // bumped on every Seek outside the buffer
	err     error    // io.EOF or a read error at nextPos
	closed  bool
}

const (
	prefetchAhead = 32 << 20
	prefetchBlock = 1 << 20
)

func openPrefetch(path string) (*prefetchFile, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	p := &prefetchFile{f: f, size: st.Size()}
	p.cond = sync.NewCond(&p.mu)
	go p.fetch()
	return p, nil
}

func (p *prefetchFile) fetch() {
	buf := make([]byte, prefetchBlock)
	for {
		p.mu.Lock()
		for !p.closed && (p.queued >= prefetchAhead || p.err != nil) {
			p.cond.Wait()
		}
		if p.closed {
			p.mu.Unlock()
			return
		}
		pos, gen := p.nextPos, p.gen
		p.mu.Unlock()

		n, err := p.f.ReadAt(buf, pos)

		p.mu.Lock()
		if gen == p.gen && !p.closed {
			if n > 0 {
				b := make([]byte, n)
				copy(b, buf[:n])
				p.blocks = append(p.blocks, b)
				p.queued += int64(n)
				p.nextPos += int64(n)
			}
			if err != nil {
				p.err = err
			}
			p.cond.Broadcast()
		}
		p.mu.Unlock()
	}
}

func (p *prefetchFile) Read(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for len(p.blocks) == 0 && p.err == nil && !p.closed {
		p.cond.Wait()
	}
	if p.closed {
		return 0, os.ErrClosed
	}
	if len(p.blocks) == 0 {
		return 0, p.err
	}
	n := 0
	for n < len(b) && len(p.blocks) > 0 {
		k := copy(b[n:], p.blocks[0])
		n += k
		if k == len(p.blocks[0]) {
			p.blocks = p.blocks[1:]
		} else {
			p.blocks[0] = p.blocks[0][k:]
		}
	}
	p.queued -= int64(n)
	p.readPos += int64(n)
	p.cond.Broadcast()
	return n, nil
}

func (p *prefetchFile) Seek(off int64, whence int) (int64, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch whence {
	case io.SeekCurrent:
		off += p.readPos
	case io.SeekEnd:
		off += p.size
	}
	if off < 0 {
		return 0, errors.New("negative seek")
	}
	if off >= p.readPos && off <= p.readPos+p.queued { // inside what is already read: just skip
		skip := off - p.readPos
		for skip > 0 {
			k := min(skip, int64(len(p.blocks[0])))
			if k == int64(len(p.blocks[0])) {
				p.blocks = p.blocks[1:]
			} else {
				p.blocks[0] = p.blocks[0][k:]
			}
			skip -= k
		}
		p.queued -= off - p.readPos
		p.readPos = off
	} else {
		p.gen++
		p.blocks, p.queued, p.err = nil, 0, nil
		p.readPos, p.nextPos = off, off
	}
	p.cond.Broadcast()
	return off, nil
}

func (p *prefetchFile) Close() error {
	p.mu.Lock()
	p.closed = true
	p.blocks = nil
	p.cond.Broadcast()
	p.mu.Unlock()
	return p.f.Close()
}
