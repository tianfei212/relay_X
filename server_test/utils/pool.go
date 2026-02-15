package utils

import "sync"

// Version: 1.0
// Developer: GPT-4/JOJO
// Date: 2026-02-15

// BufferSize 定义每个 buffer 的大小 (32KB)
const BufferSize = 32 * 1024

// BufferPool 全局内存池，用于接收和发送数据
var BufferPool = sync.Pool{
	New: func() interface{} {
		return make([]byte, BufferSize)
	},
}

// GetBuffer 从池中获取一个 32KB 的字节切片
func GetBuffer() []byte {
	return BufferPool.Get().([]byte)
}

// PutBuffer 将字节切片归还到池中
func PutBuffer(buf []byte) {
	if cap(buf) >= BufferSize {
		BufferPool.Put(buf[:BufferSize])
	}
}
