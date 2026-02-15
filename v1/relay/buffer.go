package relay

import (
	"errors"
	"io"
	"relay-x/v1/common"
	"sync"
)

// Version: 1.0
// Developer: GPT-4/JOJO
// Date: 2026-02-15

var (
	ErrBufferClosed = errors.New("buffer closed")
)

// RingBuffer 基于内存池的动态 FIFO 缓冲区
type RingBuffer struct {
	mu   sync.Mutex
	cond *sync.Cond

	buffers [][]byte // 存储数据块

	// 写指针：总是写入 buffers[len(buffers)-1] 的 writeOff 位置
	writeOff int

	// 读指针：总是读取 buffers[0] 的 readOff 位置
	readOff int

	currentSize int64 // 当前已缓冲的字节数
	maxSize     int64 // 最大容量 (bytes)

	closed      bool
	writeClosed bool // 写端关闭
}

// NewRingBuffer 创建缓冲区
func NewRingBuffer(maxSize int64) *RingBuffer {
	rb := &RingBuffer{
		maxSize: maxSize,
	}
	rb.cond = sync.NewCond(&rb.mu)
	return rb
}

// Write 实现 io.Writer
func (rb *RingBuffer) Write(p []byte) (n int, err error) {
	rb.mu.Lock()
	defer rb.mu.Unlock()

	remaining := p

	for len(remaining) > 0 {
		if rb.closed {
			return n, ErrBufferClosed
		}
		if rb.writeClosed {
			return n, ErrBufferClosed
		}

		// 检查容量
		if rb.currentSize >= rb.maxSize {
			rb.cond.Wait()
			continue
		}

		// 获取当前写入块
		var currentBuf []byte
		if len(rb.buffers) > 0 {
			currentBuf = rb.buffers[len(rb.buffers)-1]
		}

		// 如果没有块或当前块已满 (32KB)，申请新块
		if currentBuf == nil || rb.writeOff == cap(currentBuf) {
			newBuf := common.GetBuffer() // cap = 32KB
			rb.buffers = append(rb.buffers, newBuf)
			rb.writeOff = 0
			currentBuf = newBuf
		}

		// 计算可写入量
		avail := cap(currentBuf) - rb.writeOff
		toWrite := len(remaining)
		if toWrite > avail {
			toWrite = avail
		}

		// 复制数据
		copy(currentBuf[rb.writeOff:], remaining[:toWrite])

		rb.writeOff += toWrite
		rb.currentSize += int64(toWrite)
		n += toWrite
		remaining = remaining[toWrite:]

		// 唤醒读者
		rb.cond.Broadcast()
	}

	return n, nil
}

// Read 实现 io.Reader
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
		// 等待写入
		rb.cond.Wait()
	}

	// 读取逻辑
	currentBuf := rb.buffers[0]

	// 计算当前 buffer 有多少数据
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

	// 检查当前 buffer 是否读空
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
		// 归还 buffer
		common.PutBuffer(currentBuf)
		// 移除
		rb.buffers = rb.buffers[1:]
		rb.readOff = 0
		if len(rb.buffers) == 0 {
			rb.writeOff = 0
		}
	}

	// 唤醒写者 (可能有 Backpressure)
	rb.cond.Broadcast()

	return n, nil
}

// Close 关闭读写
func (rb *RingBuffer) Close() error {
	rb.mu.Lock()
	defer rb.mu.Unlock()

	if !rb.closed {
		rb.closed = true
		rb.cond.Broadcast()
		// 清理内存
		rb.cleanup()
	}
	return nil
}

// CloseWrite 关闭写入 (EOF)
func (rb *RingBuffer) CloseWrite() error {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	rb.writeClosed = true
	rb.cond.Broadcast()
	return nil
}

// Flush 立即清空缓冲区
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
