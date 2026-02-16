package relay

import (
	"errors"
	"io"
	"relay-x/v1/common"
	"sync"
)

var (
	ErrBufferClosed = errors.New("buffer closed")
)

type RingBuffer struct {
	mu   sync.Mutex
	cond *sync.Cond

	buffers [][]byte
	writeOff int
	readOff  int

	currentSize int64
	maxSize     int64

	closed      bool
	writeClosed bool
}

func NewRingBuffer(maxSize int64) *RingBuffer {
	rb := &RingBuffer{
		maxSize: maxSize,
	}
	rb.cond = sync.NewCond(&rb.mu)
	return rb
}

func (rb *RingBuffer) Write(p []byte) (n int, err error) {
	rb.mu.Lock()
	defer rb.mu.Unlock()

	remaining := p
	for len(remaining) > 0 {
		if rb.closed || rb.writeClosed {
			return n, ErrBufferClosed
		}

		if rb.currentSize >= rb.maxSize {
			rb.cond.Wait()
			continue
		}

		var currentBuf []byte
		if len(rb.buffers) > 0 {
			currentBuf = rb.buffers[len(rb.buffers)-1]
		}

		if currentBuf == nil || rb.writeOff == cap(currentBuf) {
			newBuf := common.GetBuffer()
			rb.buffers = append(rb.buffers, newBuf)
			rb.writeOff = 0
			currentBuf = newBuf
		}

		avail := cap(currentBuf) - rb.writeOff
		toWrite := len(remaining)
		if toWrite > avail {
			toWrite = avail
		}

		copy(currentBuf[rb.writeOff:], remaining[:toWrite])
		rb.writeOff += toWrite
		rb.currentSize += int64(toWrite)
		n += toWrite
		remaining = remaining[toWrite:]

		rb.cond.Broadcast()
	}

	return n, nil
}

func (rb *RingBuffer) Read(p []byte) (n int, err error) {
	rb.mu.Lock()
	defer rb.mu.Unlock()

	for {
		if rb.currentSize > 0 {
			break
		}
		if rb.closed {
			return 0, ErrBufferClosed
		}
		if rb.writeClosed {
			return 0, io.EOF
		}
		rb.cond.Wait()
	}

	currentBuf := rb.buffers[0]

	var avail int
	if len(rb.buffers) == 1 {
		avail = rb.writeOff - rb.readOff
	} else {
		avail = cap(currentBuf) - rb.readOff
	}

	toRead := len(p)
	if toRead > avail {
		toRead = avail
	}

	copy(p, currentBuf[rb.readOff:rb.readOff+toRead])
	rb.readOff += toRead
	rb.currentSize -= int64(toRead)
	n = toRead

	bufferDone := false
	if len(rb.buffers) == 1 {
		if rb.readOff == rb.writeOff {
			bufferDone = true
		}
	} else {
		if rb.readOff == cap(currentBuf) {
			bufferDone = true
		}
	}

	if bufferDone {
		common.PutBuffer(currentBuf)
		rb.buffers = rb.buffers[1:]
		rb.readOff = 0
		if len(rb.buffers) == 0 {
			rb.writeOff = 0
		}
	}

	rb.cond.Broadcast()
	return n, nil
}

func (rb *RingBuffer) Close() error {
	rb.mu.Lock()
	defer rb.mu.Unlock()

	if !rb.closed {
		rb.closed = true
		rb.cond.Broadcast()
		rb.cleanup()
	}

	return nil
}

func (rb *RingBuffer) CloseWrite() error {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	rb.writeClosed = true
	rb.cond.Broadcast()
	return nil
}

func (rb *RingBuffer) Flush() {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	rb.cleanup()
	rb.cond.Broadcast()
}

func (rb *RingBuffer) cleanup() {
	for _, b := range rb.buffers {
		common.PutBuffer(b)
	}
	rb.buffers = nil
	rb.currentSize = 0
	rb.readOff = 0
	rb.writeOff = 0
}
