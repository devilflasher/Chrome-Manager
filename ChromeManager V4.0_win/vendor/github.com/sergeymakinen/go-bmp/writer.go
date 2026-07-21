// Derived from Go which is licensed as follows:
//
// Copyright (c) 2009 The Go Authors. All rights reserved.
//
// Redistribution and use in source and binary forms, with or without
// modification, are permitted provided that the following conditions are
// met:
//
//   * Redistributions of source code must retain the above copyright
// notice, this list of conditions and the following disclaimer.
//   * Redistributions in binary form must reproduce the above
// copyright notice, this list of conditions and the following disclaimer
// in the documentation and/or other materials provided with the
// distribution.
//   * Neither the name of Google Inc. nor the names of its
// contributors may be used to endorse or promote products derived from
// this software without specific prior written permission.
//
// THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS
// "AS IS" AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT
// LIMITED TO, THE IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR
// A PARTICULAR PURPOSE ARE DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT
// OWNER OR CONTRIBUTORS BE LIABLE FOR ANY DIRECT, INDIRECT, INCIDENTAL,
// SPECIAL, EXEMPLARY, OR CONSEQUENTIAL DAMAGES (INCLUDING, BUT NOT
// LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR SERVICES; LOSS OF USE,
// DATA, OR PROFITS; OR BUSINESS INTERRUPTION) HOWEVER CAUSED AND ON ANY
// THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY, OR TORT
// (INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE
// OF THIS SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.

package bmp

import (
	"encoding/binary"
	"image"
	"io"
	"strconv"
)

func encodeSmallPaletted(w io.Writer, pix []uint8, bpp, dx, dy, stride, step int) error {
	b := make([]byte, step)
	for y := dy - 1; y >= 0; y-- {
		byte, bit := 0, 8-bpp
		for x := 0; x < dx; x++ {
			b[byte] = (b[byte] & ^((1<<bpp - 1) << bit)) | (pix[y*stride+x] << bit)
			if bit == 0 {
				bit = 8 - bpp
				byte++
			} else {
				bit -= bpp
			}
		}
		if _, err := w.Write(b); err != nil {
			return err
		}
	}
	return nil
}

func encodePaletted(w io.Writer, pix []uint8, dx, dy, stride, step int) error {
	var padding []byte
	if dx < step {
		padding = make([]byte, step-dx)
	}
	for y := dy - 1; y >= 0; y-- {
		min := y*stride + 0
		max := y*stride + dx
		if _, err := w.Write(pix[min:max]); err != nil {
			return err
		}
		if padding != nil {
			if _, err := w.Write(padding); err != nil {
				return err
			}
		}
	}
	return nil
}

func encodeRGBA(w io.Writer, pix []uint8, dx, dy, stride, step int, opaque bool) error {
	buf := make([]byte, step)
	if opaque {
		for y := dy - 1; y >= 0; y-- {
			min := y*stride + 0
			max := y*stride + dx*4
			off := 0
			for i := min; i < max; i += 4 {
				buf[off+2] = pix[i+0]
				buf[off+1] = pix[i+1]
				buf[off+0] = pix[i+2]
				off += 3
			}
			if _, err := w.Write(buf); err != nil {
				return err
			}
		}
	} else {
		for y := dy - 1; y >= 0; y-- {
			min := y*stride + 0
			max := y*stride + dx*4
			off := 0
			for i := min; i < max; i += 4 {
				a := uint32(pix[i+3])
				if a == 0 {
					buf[off+2] = 0
					buf[off+1] = 0
					buf[off+0] = 0
					buf[off+3] = 0
					off += 4
					continue
				} else if a == 0xff {
					buf[off+2] = pix[i+0]
					buf[off+1] = pix[i+1]
					buf[off+0] = pix[i+2]
					buf[off+3] = 0xff
					off += 4
					continue
				}
				buf[off+2] = uint8(((uint32(pix[i+0]) * 0xffff) / a) >> 8)
				buf[off+1] = uint8(((uint32(pix[i+1]) * 0xffff) / a) >> 8)
				buf[off+0] = uint8(((uint32(pix[i+2]) * 0xffff) / a) >> 8)
				buf[off+3] = uint8(a)
				off += 4
			}
			if _, err := w.Write(buf); err != nil {
				return err
			}
		}
	}
	return nil
}

func encodeNRGBA(w io.Writer, pix []uint8, dx, dy, stride, step int, opaque bool) error {
	buf := make([]byte, step)
	if opaque {
		for y := dy - 1; y >= 0; y-- {
			min := y*stride + 0
			max := y*stride + dx*4
			off := 0
			for i := min; i < max; i += 4 {
				buf[off+2] = pix[i+0]
				buf[off+1] = pix[i+1]
				buf[off+0] = pix[i+2]
				off += 3
			}
			if _, err := w.Write(buf); err != nil {
				return err
			}
		}
	} else {
		for y := dy - 1; y >= 0; y-- {
			min := y*stride + 0
			max := y*stride + dx*4
			off := 0
			for i := min; i < max; i += 4 {
				buf[off+2] = pix[i+0]
				buf[off+1] = pix[i+1]
				buf[off+0] = pix[i+2]
				buf[off+3] = pix[i+3]
				off += 4
			}
			if _, err := w.Write(buf); err != nil {
				return err
			}
		}
	}
	return nil
}

func encode(w io.Writer, m image.Image, step int) error {
	b := m.Bounds()
	buf := make([]byte, step)
	for y := b.Max.Y - 1; y >= b.Min.Y; y-- {
		off := 0
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, b, _ := m.At(x, y).RGBA()
			buf[off+2] = byte(r >> 8)
			buf[off+1] = byte(g >> 8)
			buf[off+0] = byte(b >> 8)
			off += 3
		}
		if _, err := w.Write(buf); err != nil {
			return err
		}
	}
	return nil
}

// Encode writes the image m to w in BMP format.
func Encode(w io.Writer, m image.Image) error {
	d := m.Bounds().Size()
	if d.X < 0 || d.Y < 0 {
		return FormatError("negative bounds")
	}
	h := struct {
		sigBM           [2]byte
		fileSize        uint32
		reserved        [2]uint16
		pixOffset       uint32
		dibHeaderSize   uint32
		width           uint32
		height          uint32
		colorPlane      uint16
		bpp             uint16
		compression     uint32
		imageSize       uint32
		xPixelsPerMeter uint32
		yPixelsPerMeter uint32
		colorUse        uint32
		colorImportant  uint32
	}{
		sigBM:         [2]byte{'B', 'M'},
		fileSize:      fileHeaderLen + infoHeaderLen,
		pixOffset:     fileHeaderLen + infoHeaderLen,
		dibHeaderSize: infoHeaderLen,
		width:         uint32(d.X),
		height:        uint32(d.Y),
		colorPlane:    1,
	}
	var step int
	var palette []byte
	var opaque bool
	switch m := m.(type) {
	case *image.Gray:
		step = (d.X + 3) &^ 3
		palette = make([]byte, 1024)
		for i := 0; i < 256; i++ {
			palette[i*4+0] = uint8(i)
			palette[i*4+1] = uint8(i)
			palette[i*4+2] = uint8(i)
			palette[i*4+3] = 0xFF
		}
		h.imageSize = uint32(d.Y * step)
		h.fileSize += uint32(len(palette)) + h.imageSize
		h.pixOffset += uint32(len(palette))
		h.bpp = 8
	case *image.Paletted:
		if len(m.Palette) == 0 || len(m.Palette) > 256 {
			return FormatError("bad palette length: " + strconv.Itoa(len(m.Palette)))
		}
		switch {
		case len(m.Palette) <= 2:
			h.bpp = 1
		case len(m.Palette) <= 4:
			h.bpp = 2
		case len(m.Palette) <= 16:
			h.bpp = 4
		default:
			h.bpp = 8
		}
		colors := 1 << h.bpp
		if len(m.Palette) < 1<<h.bpp {
			colors = len(m.Palette)
			h.colorUse = uint32(colors)
		}
		if h.bpp < 8 {
			pixelsPerByte := 8 / int(h.bpp)
			step = ((d.X+pixelsPerByte-1)/pixelsPerByte + 3) &^ 3
		} else {
			step = (d.X + 3) &^ 3
		}
		palette = make([]byte, colors*4)
		for i := 0; i < len(m.Palette) && i < 1<<h.bpp; i++ {
			r, g, b, _ := m.Palette[i].RGBA()
			palette[i*4+0] = uint8(b >> 8)
			palette[i*4+1] = uint8(g >> 8)
			palette[i*4+2] = uint8(r >> 8)
			palette[i*4+3] = 0xFF
		}
		h.imageSize = uint32(d.Y * step)
		h.fileSize += uint32(len(palette)) + h.imageSize
		h.pixOffset += uint32(len(palette))
	case *image.RGBA:
		opaque = m.Opaque()
		if opaque {
			step = (3*d.X + 3) &^ 3
			h.bpp = 24
		} else {
			step = 4 * d.X
			h.bpp = 32
		}
		h.imageSize = uint32(d.Y * step)
		h.fileSize += h.imageSize
	case *image.NRGBA:
		opaque = m.Opaque()
		if opaque {
			step = (3*d.X + 3) &^ 3
			h.bpp = 24
		} else {
			step = 4 * d.X
			h.bpp = 32
		}
		h.imageSize = uint32(d.Y * step)
		h.fileSize += h.imageSize
	default:
		step = (3*d.X + 3) &^ 3
		h.imageSize = uint32(d.Y * step)
		h.fileSize += h.imageSize
		h.bpp = 24
	}
	if err := binary.Write(w, binary.LittleEndian, h); err != nil {
		return err
	}
	if palette != nil {
		if err := binary.Write(w, binary.LittleEndian, palette); err != nil {
			return err
		}
	}
	if d.X == 0 || d.Y == 0 {
		return nil
	}
	switch m := m.(type) {
	case *image.Gray:
		return encodePaletted(w, m.Pix, d.X, d.Y, m.Stride, step)
	case *image.Paletted:
		if h.bpp < 8 {
			return encodeSmallPaletted(w, m.Pix, int(h.bpp), d.X, d.Y, m.Stride, step)
		}
		return encodePaletted(w, m.Pix, d.X, d.Y, m.Stride, step)
	case *image.RGBA:
		return encodeRGBA(w, m.Pix, d.X, d.Y, m.Stride, step, opaque)
	case *image.NRGBA:
		return encodeNRGBA(w, m.Pix, d.X, d.Y, m.Stride, step, opaque)
	}
	return encode(w, m, step)
}
