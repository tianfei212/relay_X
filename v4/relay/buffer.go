package relay

import "sync"

var bufPool = sync.Pool{
	New: func() any {
		b := make([]byte, 64*1024)
		return &b
	},
}

// getBuf 从缓冲池获取一个可复用的读写缓冲区。
func getBuf() []byte {
	p := bufPool.Get().(*[]byte)
	return *p
}

// putBuf 将缓冲区放回缓冲池（会忽略异常小的切片）。
// 参数：
// - b: 待回收缓冲区
func putBuf(b []byte) {
	if cap(b) < 4096 {
		return
	}
	b = b[:cap(b)]
	bufPool.Put(&b)
}
