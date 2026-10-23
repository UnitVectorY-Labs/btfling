// Package ble provides a bounded, full-duplex byte stream over BLE GATT.
package ble

import (
	"encoding/binary"
	"errors"
)

const (
	WireVersion  = 1
	HeaderSize   = 36
	WindowSize   = 64 * 1024
	maxValueSize = 512 // GATT attribute value bound, not an assumed ATT MTU.
	flagOpen     = 1
	flagFIN      = 2
	flagFINACK   = 4
)

type frame struct {
	flags                       byte
	session, offset, ack, limit uint64
	data                        []byte
}

func payloadSize(value int) (int, error) {
	value = min(value, maxValueSize)
	if value <= HeaderSize {
		return 0, errors.New("BLE negotiated value is too small: streaming requires at least 37 bytes")
	}
	return value - HeaderSize, nil
}
func (f frame) encode(p []byte) []byte {
	p = p[:HeaderSize+len(f.data)]
	p[0], p[1], p[2], p[3] = WireVersion, f.flags, 0, 0
	binary.BigEndian.PutUint64(p[4:12], f.session)
	binary.BigEndian.PutUint64(p[12:20], f.offset)
	binary.BigEndian.PutUint64(p[20:28], f.ack)
	binary.BigEndian.PutUint64(p[28:36], f.limit)
	copy(p[36:], f.data)
	return p
}
func decodeFrame(p []byte) (frame, error) {
	if len(p) < HeaderSize || len(p) > maxValueSize {
		return frame{}, errors.New("invalid BLE frame length")
	}
	if p[0] != WireVersion {
		return frame{}, errors.New("unsupported BLE protocol version")
	}
	f := frame{flags: p[1], session: binary.BigEndian.Uint64(p[4:12]), offset: binary.BigEndian.Uint64(p[12:20]), ack: binary.BigEndian.Uint64(p[20:28]), limit: binary.BigEndian.Uint64(p[28:36]), data: p[36:]}
	if p[2] != 0 || p[3] != 0 || f.flags & ^byte(7) != 0 || f.session == 0 || (f.flags&flagOpen != 0 && (f.flags != flagOpen || len(f.data) != 0 || f.offset != 0 || f.ack != 0)) {
		return frame{}, errors.New("invalid BLE frame header")
	}
	return f, nil
}
