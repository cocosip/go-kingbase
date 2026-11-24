// Package sm3 implements the SM3 cryptographic hash function
// (GB/T 32905-2016). It provides hash.Hash so it can be used with hmac and
// pbkdf2, and it is independent of any vendor implementation.
package sm3

import (
	"encoding/binary"
	"hash"
	"math/bits"
)

// Size is the SM3 digest size in bytes.
const Size = 32

// BlockSize is the SM3 block size in bytes.
const BlockSize = 64

var iv = [8]uint32{
	0x7380166f, 0x4914b2b9, 0x172442d7, 0xda8a0600,
	0xa96f30bc, 0x163138aa, 0xe38dee4d, 0xb0fb0e4e,
}

// New returns a new SM3 hash.
func New() hash.Hash {
	d := &digest{}
	d.Reset()
	return d
}

type digest struct {
	state  [8]uint32
	buf    [BlockSize]byte
	bufLen int
	length uint64
}

// Reset resets the hash to its initial state.
func (d *digest) Reset() {
	d.state = iv
	d.bufLen = 0
	d.length = 0
}

// Size returns the digest size.
func (d *digest) Size() int {
	return Size
}

// BlockSize returns the block size.
func (d *digest) BlockSize() int {
	return BlockSize
}

// Write adds data to the running hash.
func (d *digest) Write(p []byte) (int, error) {
	written := len(p)
	d.length += uint64(written)
	if d.bufLen > 0 {
		copied := copy(d.buf[d.bufLen:], p)
		d.bufLen += copied
		p = p[copied:]
		if d.bufLen == BlockSize {
			d.compress(d.buf[:])
			d.bufLen = 0
		}
	}
	for len(p) >= BlockSize {
		d.compress(p[:BlockSize])
		p = p[BlockSize:]
	}
	if len(p) > 0 {
		d.bufLen = copy(d.buf[:], p)
	}
	return written, nil
}

// Sum appends the current digest to b without changing the hash state.
func (d *digest) Sum(b []byte) []byte {
	clone := *d
	bitLength := clone.length * 8

	var padding [BlockSize]byte
	padding[0] = 0x80
	padLen := 56 - int(clone.length%BlockSize)
	if padLen <= 0 {
		padLen += BlockSize
	}
	if _, err := clone.Write(padding[:padLen]); err != nil {
		panic(err)
	}
	var lengthBytes [8]byte
	binary.BigEndian.PutUint64(lengthBytes[:], bitLength)
	if _, err := clone.Write(lengthBytes[:]); err != nil {
		panic(err)
	}

	var out [Size]byte
	for i, v := range clone.state {
		binary.BigEndian.PutUint32(out[i*4:], v)
	}
	return append(b, out[:]...)
}

func (d *digest) compress(block []byte) {
	var w [68]uint32
	var w1 [64]uint32
	for i := 0; i < 16; i++ {
		w[i] = binary.BigEndian.Uint32(block[i*4:])
	}
	for i := 16; i < 68; i++ {
		w[i] = p1(w[i-16]^w[i-9]^bits.RotateLeft32(w[i-3], 15)) ^
			bits.RotateLeft32(w[i-13], 7) ^ w[i-6]
	}
	for i := 0; i < 64; i++ {
		w1[i] = w[i] ^ w[i+4]
	}

	a, b, c, dd := d.state[0], d.state[1], d.state[2], d.state[3]
	e, f, g, h := d.state[4], d.state[5], d.state[6], d.state[7]
	for j := 0; j < 64; j++ {
		constant := uint32(0x79cc4519)
		var ff, gg uint32
		if j < 16 {
			ff = a ^ b ^ c
			gg = e ^ f ^ g
		} else {
			constant = 0x7a879d8a
			ff = (a & b) | (a & c) | (b & c)
			gg = (e & f) | (^e & g)
		}
		ss1 := bits.RotateLeft32(bits.RotateLeft32(a, 12)+e+bits.RotateLeft32(constant, j%32), 7)
		ss2 := ss1 ^ bits.RotateLeft32(a, 12)
		tt1 := ff + dd + ss2 + w1[j]
		tt2 := gg + h + ss1 + w[j]
		dd = c
		c = bits.RotateLeft32(b, 9)
		b = a
		a = tt1
		h = g
		g = bits.RotateLeft32(f, 19)
		f = e
		e = p0(tt2)
	}

	d.state[0] ^= a
	d.state[1] ^= b
	d.state[2] ^= c
	d.state[3] ^= dd
	d.state[4] ^= e
	d.state[5] ^= f
	d.state[6] ^= g
	d.state[7] ^= h
}

func p0(x uint32) uint32 {
	return x ^ bits.RotateLeft32(x, 9) ^ bits.RotateLeft32(x, 17)
}

func p1(x uint32) uint32 {
	return x ^ bits.RotateLeft32(x, 15) ^ bits.RotateLeft32(x, 23)
}
