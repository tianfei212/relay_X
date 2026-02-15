package common

import "sync"

// Version: 1.0
// Developer: GPT-4/JOJO
// Date: 2026-02-15

// BufferSize 定义每个 buffer 的大小 (32KB)
const BufferSize = 32 * 1024

// bufferPool 全局内存池，用于 io.CopyBuffer
var bufferPool = sync.Pool{
	New: func() interface{} {
		// 分配 32KB 内存
		return make([]byte, BufferSize)
	},
}

// GetBuffer 从池中获取一个 32KB 的字节切片
// 功能说明: 获取预分配内存，减少 GC 压力
// 返回: []byte
func GetBuffer() []byte {
	return bufferPool.Get().([]byte)
}

// PutBuffer 将字节切片归还到池中
// 功能说明: 归还内存
// 参数: buf []byte
func PutBuffer(buf []byte) {
	// 只有大小符合的才放回去，防止污染
	if cap(buf) >= BufferSize {
		bufferPool.Put(buf[:BufferSize])
	}
}
