package mp4s

import (
	"bytes"
	"encoding/binary"
	"time"
)

// GenWithDuration 生成指定时长的 MP4 视频数据
func GenWithDuration(d time.Duration) []byte {
	var buf bytes.Buffer

	writeBox(&buf, "ftyp", func(b *bytes.Buffer) {
		b.WriteString("isom")
		b.Write([]byte{0x00, 0x00, 0x00, 0x01})
		b.WriteString("isom")
		b.WriteString("iso2")
	})

	writeBox(&buf, "moov", func(moov *bytes.Buffer) {
		// mvhd (全局影片时长)
		writeBox(moov, "mvhd", func(b *bytes.Buffer) {
			b.WriteByte(0x00)                                           // 版本
			b.Write([]byte{0x00, 0x00, 0x00})                           // 标志位
			b.Write(make([]byte, 4))                                    // 创建时间
			b.Write(make([]byte, 4))                                    // 修改时间
			binary.Write(b, binary.BigEndian, uint32(1000))             // 时间刻度
			binary.Write(b, binary.BigEndian, uint32(d.Milliseconds())) // duration: 视频时长
			binary.Write(b, binary.BigEndian, uint32(0x00010000))       // 播放速率 1.0
			binary.Write(b, binary.BigEndian, uint16(0x0100))           // 音量 1.0
			b.Write(make([]byte, 10))                                   // 保留字段
			binary.Write(b, binary.BigEndian, [9]uint32{
				0x00010000, 0, 0,
				0, 0x00010000, 0,
				0, 0, 0x40000000,
			}) // 单位矩阵
			b.Write(make([]byte, 24))                    // 预定义字段
			binary.Write(b, binary.BigEndian, uint32(2)) // 下一个 track ID
		})

		// trak (伪轨道)
		writeBox(moov, "trak", func(trak *bytes.Buffer) {
			// tkhd (轨道头)
			writeBox(trak, "tkhd", func(b *bytes.Buffer) {
				b.WriteByte(0x00)
				b.Write([]byte{0x00, 0x00, 0x07})                           // 标志位: 轨道启用, 位于影片和预览中
				b.Write(make([]byte, 4))                                    // 创建时间
				b.Write(make([]byte, 4))                                    // 修改时间
				binary.Write(b, binary.BigEndian, uint32(1))                // track_ID
				b.Write(make([]byte, 4))                                    // 保留字段
				binary.Write(b, binary.BigEndian, uint32(d.Milliseconds())) // duration
				b.Write(make([]byte, 8))                                    // 保留字段
				binary.Write(b, binary.BigEndian, uint16(0))                // 图层
				binary.Write(b, binary.BigEndian, uint16(0))                // 备用分组
				binary.Write(b, binary.BigEndian, uint16(0))                // 音量
				b.Write([]byte{0x00, 0x00})                                 // 保留字段
				binary.Write(b, binary.BigEndian, [9]uint32{
					0x00010000, 0, 0,
					0, 0x00010000, 0,
					0, 0, 0x40000000,
				}) // 矩阵
				binary.Write(b, binary.BigEndian, uint32(0)) // 宽度
				binary.Write(b, binary.BigEndian, uint32(0)) // 高度
			})

			// mdia
			writeBox(trak, "mdia", func(mdia *bytes.Buffer) {
				// mdhd
				writeBox(mdia, "mdhd", func(b *bytes.Buffer) {
					b.WriteByte(0x00)
					b.Write([]byte{0x00, 0x00, 0x00})
					b.Write(make([]byte, 4))                                    // 创建时间
					b.Write(make([]byte, 4))                                    // 修改时间
					binary.Write(b, binary.BigEndian, uint32(1000))             // 时间刻度
					binary.Write(b, binary.BigEndian, uint32(d.Milliseconds())) // duration
					binary.Write(b, binary.BigEndian, uint16(0x55c4))           // language = und (ISO-639-2/T code)
					b.Write([]byte{0x00, 0x00})                                 // 预定义字段
				})

				// hdlr (handler type: vide)
				writeBox(mdia, "hdlr", func(b *bytes.Buffer) {
					b.Write([]byte{0x00, 0x00, 0x00, 0x00}) // 版本 + 标志位
					b.Write(make([]byte, 4))                // 预定义字段
					b.Write([]byte("vide"))                 // handler_type
					b.Write(make([]byte, 12))               // 保留字段
					b.WriteString("Fake Video Handler")     // 名称
					b.WriteByte(0x00)                       // 结束符
				})

				// minf（可选, 不加也能解析）
			})
		})
	})

	return buf.Bytes()
}

func writeBox(parent *bytes.Buffer, boxType string, writePayload func(*bytes.Buffer)) {
	var payload bytes.Buffer
	writePayload(&payload)

	size := uint32(8 + payload.Len())
	binary.Write(parent, binary.BigEndian, size)
	parent.WriteString(boxType)
	parent.Write(payload.Bytes())
}
