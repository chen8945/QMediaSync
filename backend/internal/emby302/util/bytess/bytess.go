package bytess

import "sync"

const CommonBufferSize = 32 * 1024

var commonBufferPool = sync.Pool{
	New: func() any {
		buf := Buffer(make([]byte, CommonBufferSize))
		return &buf
	},
}

// Buffer 具备回收功能的缓冲区包装类型
type Buffer []byte

// PutBack 将缓冲区放回池中
func (b *Buffer) PutBack() {
	commonBufferPool.Put(b)
}

// Bytes 获取缓冲区字节切片
func (b *Buffer) Bytes() []byte {
	return *b
}

// CommonFixedBuffer 获取一个可复用的固定大小的缓冲区
func CommonFixedBuffer() *Buffer {
	return commonBufferPool.Get().(*Buffer)
}
