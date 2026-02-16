package video

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"io"
	"time"
)

const (
	magic   uint32 = 0x56345654
	version uint8  = 1
)

type MessageType uint8

const (
	MsgData MessageType = 1
	MsgEcho MessageType = 2
	MsgPing MessageType = 3
	MsgPong MessageType = 4
)

type Frame struct {
	Type MessageType
	Seq  uint64

	SentUnixNs       int64
	ServerRecvUnixNs int64
	ServerSendUnixNs int64

	CRC32 uint32

	Payload []byte
}

func WriteDataFrame(w io.Writer, seq uint64, sent time.Time, payload []byte) error {
	if len(payload) > int(^uint32(0)) {
		return fmt.Errorf("payload too large: %d", len(payload))
	}
	hdr := make([]byte, 4+1+1+2+8+8+8+8+4+4)
	off := 0
	binary.BigEndian.PutUint32(hdr[off:], magic)
	off += 4
	hdr[off] = version
	off++
	hdr[off] = byte(MsgData)
	off++
	off += 2
	binary.BigEndian.PutUint64(hdr[off:], seq)
	off += 8
	binary.BigEndian.PutUint64(hdr[off:], uint64(sent.UnixNano()))
	off += 8
	binary.BigEndian.PutUint64(hdr[off:], 0)
	off += 8
	binary.BigEndian.PutUint64(hdr[off:], 0)
	off += 8
	cr := crc32.ChecksumIEEE(payload)
	binary.BigEndian.PutUint32(hdr[off:], cr)
	off += 4
	binary.BigEndian.PutUint32(hdr[off:], uint32(len(payload)))

	if _, err := w.Write(hdr); err != nil {
		return err
	}
	if len(payload) == 0 {
		return nil
	}
	_, err := w.Write(payload)
	return err
}

func WriteEchoFrame(w io.Writer, seq uint64, sentUnixNs, serverRecvUnixNs, serverSendUnixNs int64, payload []byte) error {
	if len(payload) > int(^uint32(0)) {
		return fmt.Errorf("payload too large: %d", len(payload))
	}
	hdr := make([]byte, 4+1+1+2+8+8+8+8+4+4)
	off := 0
	binary.BigEndian.PutUint32(hdr[off:], magic)
	off += 4
	hdr[off] = version
	off++
	hdr[off] = byte(MsgEcho)
	off++
	off += 2
	binary.BigEndian.PutUint64(hdr[off:], seq)
	off += 8
	binary.BigEndian.PutUint64(hdr[off:], uint64(sentUnixNs))
	off += 8
	binary.BigEndian.PutUint64(hdr[off:], uint64(serverRecvUnixNs))
	off += 8
	binary.BigEndian.PutUint64(hdr[off:], uint64(serverSendUnixNs))
	off += 8
	cr := crc32.ChecksumIEEE(payload)
	binary.BigEndian.PutUint32(hdr[off:], cr)
	off += 4
	binary.BigEndian.PutUint32(hdr[off:], uint32(len(payload)))

	if _, err := w.Write(hdr); err != nil {
		return err
	}
	if len(payload) == 0 {
		return nil
	}
	_, err := w.Write(payload)
	return err
}

func WritePingFrame(w io.Writer, seq uint64, sent time.Time) error {
	hdr := make([]byte, 4+1+1+2+8+8+8+8+4+4)
	off := 0
	binary.BigEndian.PutUint32(hdr[off:], magic)
	off += 4
	hdr[off] = version
	off++
	hdr[off] = byte(MsgPing)
	off++
	off += 2
	binary.BigEndian.PutUint64(hdr[off:], seq)
	off += 8
	binary.BigEndian.PutUint64(hdr[off:], uint64(sent.UnixNano()))
	off += 8
	binary.BigEndian.PutUint64(hdr[off:], 0)
	off += 8
	binary.BigEndian.PutUint64(hdr[off:], 0)
	off += 8
	binary.BigEndian.PutUint32(hdr[off:], 0)
	off += 4
	binary.BigEndian.PutUint32(hdr[off:], 0)
	_, err := w.Write(hdr)
	return err
}

func WritePongFrame(w io.Writer, seq uint64, sentUnixNs, serverRecvUnixNs, serverSendUnixNs int64) error {
	hdr := make([]byte, 4+1+1+2+8+8+8+8+4+4)
	off := 0
	binary.BigEndian.PutUint32(hdr[off:], magic)
	off += 4
	hdr[off] = version
	off++
	hdr[off] = byte(MsgPong)
	off++
	off += 2
	binary.BigEndian.PutUint64(hdr[off:], seq)
	off += 8
	binary.BigEndian.PutUint64(hdr[off:], uint64(sentUnixNs))
	off += 8
	binary.BigEndian.PutUint64(hdr[off:], uint64(serverRecvUnixNs))
	off += 8
	binary.BigEndian.PutUint64(hdr[off:], uint64(serverSendUnixNs))
	off += 8
	binary.BigEndian.PutUint32(hdr[off:], 0)
	off += 4
	binary.BigEndian.PutUint32(hdr[off:], 0)
	_, err := w.Write(hdr)
	return err
}

func ReadFrame(r *bufio.Reader, maxPayloadBytes int) (Frame, error) {
	hdr := make([]byte, 4+1+1+2+8+8+8+8+4+4)
	if _, err := io.ReadFull(r, hdr); err != nil {
		return Frame{}, err
	}
	off := 0
	if binary.BigEndian.Uint32(hdr[off:]) != magic {
		return Frame{}, fmt.Errorf("invalid magic")
	}
	off += 4
	if hdr[off] != version {
		return Frame{}, fmt.Errorf("unsupported version: %d", hdr[off])
	}
	off++
	typ := MessageType(hdr[off])
	off++
	off += 2
	seq := binary.BigEndian.Uint64(hdr[off:])
	off += 8
	sent := int64(binary.BigEndian.Uint64(hdr[off:]))
	off += 8
	srvRecv := int64(binary.BigEndian.Uint64(hdr[off:]))
	off += 8
	srvSend := int64(binary.BigEndian.Uint64(hdr[off:]))
	off += 8
	cr := binary.BigEndian.Uint32(hdr[off:])
	off += 4
	n := int(binary.BigEndian.Uint32(hdr[off:]))

	if n < 0 || (maxPayloadBytes > 0 && n > maxPayloadBytes) {
		return Frame{}, fmt.Errorf("payload too large: %d", n)
	}
	var payload []byte
	if n > 0 {
		payload = make([]byte, n)
		if _, err := io.ReadFull(r, payload); err != nil {
			return Frame{}, err
		}
		if crc32.ChecksumIEEE(payload) != cr {
			return Frame{}, fmt.Errorf("crc mismatch")
		}
	}
	return Frame{
		Type:             typ,
		Seq:              seq,
		SentUnixNs:       sent,
		ServerRecvUnixNs: srvRecv,
		ServerSendUnixNs: srvSend,
		CRC32:            cr,
		Payload:          payload,
	}, nil
}
