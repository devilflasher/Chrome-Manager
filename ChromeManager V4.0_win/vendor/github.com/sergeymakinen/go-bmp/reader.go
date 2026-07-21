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

// Package bmp implements a BMP image decoder and encoder.
//
// The BMP specification is at http://www.digicamsoft.com/bmp/bmp.html.
package bmp

import (
	"image"
	"image/color"
	"io"
	"strconv"
)

const (
	fileHeaderLen = 14
	infoHeaderLen = 40
)

// FormatError reports that the input is not a valid BMP.
type FormatError string

func (e FormatError) Error() string { return "bmp: invalid format: " + string(e) }

// UnsupportedError reports that the input uses a valid but unimplemented BMP feature.
type UnsupportedError string

func (e UnsupportedError) Error() string { return "bmp: unsupported feature: " + string(e) }

func readUint16(b []byte) uint16 {
	return uint16(b[0]) | uint16(b[1])<<8
}

func readUint32(b []byte) uint32 {
	return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24
}

type decoder struct {
	r                             io.Reader
	c                             image.Config
	bpp                           uint16
	topDown, rgb565, noAlpha, rle bool
}

func (d *decoder) DecodeConfig() error {
	const (
		v4InfoHeaderLen = 108
		v5InfoHeaderLen = 124
	)
	const (
		biRGB       = 0
		biRLE8      = 1
		biRLE4      = 2
		biBitFields = 3
	)
	// We only support those BMP images that are a BITMAPFILEHEADER
	// immediately followed by a BITMAPINFOHEADER.
	var b [1024]byte
	if _, err := io.ReadFull(d.r, b[:fileHeaderLen+4]); err != nil {
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
		return err
	}
	if string(b[:2]) != "BM" {
		return FormatError("not a BMP file")
	}
	offset := readUint32(b[10:])
	infoLen := readUint32(b[14:])
	if infoLen != infoHeaderLen && infoLen != v4InfoHeaderLen && infoLen != v5InfoHeaderLen {
		return UnsupportedError("DIB header version")
	}
	if _, err := io.ReadFull(d.r, b[fileHeaderLen+4:fileHeaderLen+infoLen]); err != nil {
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
		return err
	}
	width := int(int32(readUint32(b[18:])))
	height := int(int32(readUint32(b[22:])))
	if height < 0 {
		height, d.topDown = -height, true
	}
	if width < 0 || height < 0 {
		return UnsupportedError("non-positive dimension")
	}
	if planes := readUint16(b[26:]); planes != 1 {
		return UnsupportedError("planes " + strconv.FormatUint(uint64(planes), 10))
	}
	d.bpp = readUint16(b[28:])
	compression, colors := readUint32(b[30:]), readUint32(b[46:])
	colorMaskLen := uint32(0)
	switch {
	case compression == biBitFields:
		if infoLen == infoHeaderLen {
			colorMaskLen = 4 * 3
			if _, err := io.ReadFull(d.r, b[fileHeaderLen+infoLen:fileHeaderLen+infoLen+colorMaskLen]); err != nil {
				if err == io.EOF {
					err = io.ErrUnexpectedEOF
				}
				return err
			}
		}
		switch {
		case d.bpp == 16 && readUint32(b[54:]) == 0xF800 && readUint32(b[58:]) == 0x7E0 && readUint32(b[62:]) == 0x1F:
			// RGB565
			d.rgb565 = true
			fallthrough
		case d.bpp == 16 && readUint32(b[54:]) == 0x7C00 && readUint32(b[58:]) == 0x3E0 && readUint32(b[62:]) == 0x1F:
			// RGB555
			fallthrough
		case d.bpp == 32 && readUint32(b[54:]) == 0xFF0000 && readUint32(b[58:]) == 0xFF00 && readUint32(b[62:]) == 0xFF &&
			(infoLen == infoHeaderLen || readUint32(b[66:]) == 0xFF000000):
			// If compression is set to BITFIELDS, but the bitmask is set to the default bitmask
			// that would be used if compression was set to 0, we can continue as if compression was 0.
			compression = biRGB
			// Also disable the alpha for 32 bit-per-pixel images if the mask was used with BITMAPINFOHEADER.
			if infoLen == infoHeaderLen {
				d.noAlpha = true
			}
		}
	case ((d.bpp == 4 && compression == biRLE4) || (d.bpp == 8 && compression == biRLE8)) && !d.topDown:
		d.rle = true
		compression = biRGB
	}
	if compression != biRGB {
		return UnsupportedError("compression method")
	}
	switch d.bpp {
	case 1, 2, 4, 8:
		if colors == 0 {
			colors = 1 << d.bpp
		}
		if offset != fileHeaderLen+infoLen+colors*4 {
			return UnsupportedError("bitmap offset")
		}
		if _, err := io.ReadFull(d.r, b[:colors*4]); err != nil {
			return err
		}
		pcm := make(color.Palette, colors)
		for i := range pcm {
			// BMP images are stored in BGR order rather than RGB order.
			// Every 4th byte is padding.
			pcm[i] = color.RGBA{b[4*i+2], b[4*i+1], b[4*i+0], 0xFF}
		}
		d.c = image.Config{
			ColorModel: pcm,
			Width:      width,
			Height:     height,
		}
		return nil
	case 16:
		if offset != fileHeaderLen+infoLen+colorMaskLen {
			return UnsupportedError("bitmap offset")
		}
		d.c = image.Config{
			ColorModel: color.RGBAModel,
			Width:      width,
			Height:     height,
		}
		return nil
	case 24, 32:
		if offset != fileHeaderLen+infoLen+colorMaskLen {
			return UnsupportedError("bitmap offset")
		}
		d.c = image.Config{
			ColorModel: color.RGBAModel,
			Width:      width,
			Height:     height,
		}
		return nil
	default:
		return UnsupportedError("bit depth " + strconv.FormatUint(uint64(d.bpp), 10))
	}
}

func (d *decoder) Decode() (image.Image, error) {
	if d.rle {
		return d.decodeRLE()
	}
	switch d.bpp {
	case 1, 2, 4:
		return d.decodeSmallPaletted()
	case 8:
		return d.decodePaletted()
	case 16:
		return d.decodeRGB5x5()
	case 24:
		return d.decodeRGB()
	case 32:
		return d.decodeNRGBA()
	}
	panic("unreachable")
}

// decodeSmallPaletted reads a bpp (< 8) bit-per-pixel BMP image from d.r.
// If d.topDown is false, the image rows will be read bottom-up.
func (d *decoder) decodeSmallPaletted() (image.Image, error) {
	paletted := image.NewPaletted(image.Rect(0, 0, d.c.Width, d.c.Height), d.c.ColorModel.(color.Palette))
	if d.c.Width == 0 || d.c.Height == 0 {
		return paletted, nil
	}
	// There are specified bpp bits per pixel, and each row is 4-byte aligned.
	pixelsPerByte := 8 / int(d.bpp)
	b := make([]byte, ((d.c.Width+pixelsPerByte-1)/pixelsPerByte+3)&^3)
	y0, y1, yDelta := d.c.Height-1, -1, -1
	if d.topDown {
		y0, y1, yDelta = 0, d.c.Height, +1
	}
	for y := y0; y != y1; y += yDelta {
		p := paletted.Pix[y*paletted.Stride : y*paletted.Stride+d.c.Width]
		if _, err := io.ReadFull(d.r, b); err != nil {
			return nil, err
		}
		byte, bit := 0, 8-d.bpp
		for x := 0; x < d.c.Width; x++ {
			p[x] = (b[byte] >> bit) & (1<<d.bpp - 1)
			if bit == 0 {
				bit = 8 - d.bpp
				byte++
			} else {
				bit -= d.bpp
			}
		}
	}
	return paletted, nil
}

// decodePaletted reads an 8 bit-per-pixel BMP image from d.r.
// If d.topDown is false, the image rows will be read bottom-up.
func (d *decoder) decodePaletted() (image.Image, error) {
	paletted := image.NewPaletted(image.Rect(0, 0, d.c.Width, d.c.Height), d.c.ColorModel.(color.Palette))
	if d.c.Width == 0 || d.c.Height == 0 {
		return paletted, nil
	}
	var tmp [4]byte
	y0, y1, yDelta := d.c.Height-1, -1, -1
	if d.topDown {
		y0, y1, yDelta = 0, d.c.Height, +1
	}
	for y := y0; y != y1; y += yDelta {
		p := paletted.Pix[y*paletted.Stride : y*paletted.Stride+d.c.Width]
		if _, err := io.ReadFull(d.r, p); err != nil {
			return nil, err
		}
		// Each row is 4-byte aligned.
		if d.c.Width%4 != 0 {
			_, err := io.ReadFull(d.r, tmp[:4-d.c.Width%4])
			if err != nil {
				return nil, err
			}
		}
	}
	return paletted, nil
}

// decodeRLE reads an 4 or 8 bit-per-pixel RLE-encoded BMP image from d.r.
func (d *decoder) decodeRLE() (image.Image, error) {
	paletted := image.NewPaletted(image.Rect(0, 0, d.c.Width, d.c.Height), d.c.ColorModel.(color.Palette))
	if d.c.Width == 0 || d.c.Height == 0 {
		return paletted, nil
	}
	var b [256]byte
	read := func() (byte, byte, error) {
		if _, err := io.ReadFull(d.r, b[:2]); err != nil {
			return 0, 0, err
		}
		return b[0], b[1], nil
	}
	x, y := 0, d.c.Height-1
	isValid := func() bool { return x >= 0 && x < paletted.Stride && y >= 0 && y < d.c.Height }
Loop:
	for {
		b1, b2, err := read()
		if err != nil {
			return nil, err
		}
		switch b1 {
		case 0:
			switch b2 {
			case 0:
				// EOL.
				x, y = 0, y-1
				if !isValid() {
					return nil, FormatError("invalid RLE data")
				}
			case 1:
				// EOF.
				break Loop
			case 2:
				// Delta.
				b1, b2, err := read()
				if err != nil {
					return nil, err
				}
				x, y = x+int(b1), y-int(b2)
				if !isValid() {
					return nil, FormatError("invalid RLE data")
				}
			default:
				// Absolute mode.
				n := (uint16(b2)*d.bpp + 8 - 1) / 8
				if (d.bpp == 8 && b2&0x1 != 0) || (d.bpp == 4 && ((b2&0x3 == 1) || (b2&0x3 == 2))) {
					n++
				}
				if _, err := io.ReadFull(d.r, b[:n]); err != nil {
					return nil, err
				}
				for i, j := uint8(0), 0; i < b2; i++ {
					var c byte
					if d.bpp == 8 {
						c = b[i]
					} else {
						c = (b[j] >> 4) & 0xF
					}
					if !isValid() {
						return nil, FormatError("invalid RLE data")
					}
					paletted.Pix[y*paletted.Stride+x] = c
					x++
					if d.bpp == 4 {
						if i++; i < b2 {
							if !isValid() {
								return nil, FormatError("invalid RLE data")
							}
							paletted.Pix[y*paletted.Stride+x] = b[j] & 0xF
							x++
						}
						if i%2 != 0 {
							j++
						}
					}
				}
			}
		default:
			// Encoded mode.
			// TODO(sergeymakinen): Consider ignoring pixels past the end of the row.
			for i := uint8(0); i < b1; i++ {
				if !isValid() {
					return nil, FormatError("invalid RLE data")
				}
				var c byte
				if d.bpp == 8 {
					c = b2
				} else {
					if i%2 == 0 {
						c = (b2 >> 4) & 0xF
					} else {
						c = b2 & 0xF
					}
				}
				paletted.Pix[y*paletted.Stride+x] = c
				x++
			}
		}
	}
	return paletted, nil
}

// decodeRGB5x5 reads a 16 bit-per-pixel BMP image from d.r.
// If d.topDown is false, the image rows will be read bottom-up.
// If d.rgb565 is true, the image will be read as RGB565, otherwise as RGB555.
func (d *decoder) decodeRGB5x5() (image.Image, error) {
	rgba := image.NewRGBA(image.Rect(0, 0, d.c.Width, d.c.Height))
	if d.c.Width == 0 || d.c.Height == 0 {
		return rgba, nil
	}
	// There are 2 bytes per pixel, and each row is 4-byte aligned.
	b := make([]byte, (2*d.c.Width+3)&^3)
	y0, y1, yDelta := d.c.Height-1, -1, -1
	if d.topDown {
		y0, y1, yDelta = 0, d.c.Height, +1
	}
	for y := y0; y != y1; y += yDelta {
		if _, err := io.ReadFull(d.r, b); err != nil {
			return nil, err
		}
		p := rgba.Pix[y*rgba.Stride : y*rgba.Stride+d.c.Width*4]
		for i, j := 0, 0; i < len(p); i, j = i+4, j+2 {
			pixel := readUint16(b[j:])
			if d.rgb565 {
				p[i+0] = uint8((pixel&0xF800)>>11) << 3
				p[i+1] = uint8((pixel&0x7E0)>>5) << 2
			} else {
				p[i+0] = uint8((pixel&0x7C00)>>10) << 3
				p[i+1] = uint8((pixel&0x3E0)>>5) << 3
			}
			p[i+2] = uint8(pixel&0x1F) << 3
			p[i+3] = 0xFF
		}
	}
	return rgba, nil
}

// decodeRGB reads a 24 bit-per-pixel BMP image from d.r.
// If d.topDown is false, the image rows will be read bottom-up.
func (d *decoder) decodeRGB() (image.Image, error) {
	rgba := image.NewRGBA(image.Rect(0, 0, d.c.Width, d.c.Height))
	if d.c.Width == 0 || d.c.Height == 0 {
		return rgba, nil
	}
	// There are 3 bytes per pixel, and each row is 4-byte aligned.
	b := make([]byte, (3*d.c.Width+3)&^3)
	y0, y1, yDelta := d.c.Height-1, -1, -1
	if d.topDown {
		y0, y1, yDelta = 0, d.c.Height, +1
	}
	for y := y0; y != y1; y += yDelta {
		if _, err := io.ReadFull(d.r, b); err != nil {
			return nil, err
		}
		p := rgba.Pix[y*rgba.Stride : y*rgba.Stride+d.c.Width*4]
		for i, j := 0, 0; i < len(p); i, j = i+4, j+3 {
			// BMP images are stored in BGR order rather than RGB order.
			p[i+0] = b[j+2]
			p[i+1] = b[j+1]
			p[i+2] = b[j+0]
			p[i+3] = 0xFF
		}
	}
	return rgba, nil
}

// decodeNRGBA reads a 32 bit-per-pixel BMP image from d.r.
// If d.topDown is false, the image rows will be read bottom-up.
// If d.noAlpha is true, the image will have the alpha forcibly set to 0xFF.
func (d *decoder) decodeNRGBA() (image.Image, error) {
	rgba := image.NewNRGBA(image.Rect(0, 0, d.c.Width, d.c.Height))
	if d.c.Width == 0 || d.c.Height == 0 {
		return rgba, nil
	}
	y0, y1, yDelta := d.c.Height-1, -1, -1
	if d.topDown {
		y0, y1, yDelta = 0, d.c.Height, +1
	}
	for y := y0; y != y1; y += yDelta {
		p := rgba.Pix[y*rgba.Stride : y*rgba.Stride+d.c.Width*4]
		if _, err := io.ReadFull(d.r, p); err != nil {
			return nil, err
		}
		for i := 0; i < len(p); i += 4 {
			// BMP images are stored in BGRA order rather than RGBA order.
			p[i+0], p[i+2] = p[i+2], p[i+0]
			if d.noAlpha {
				p[i+3] = 0xFF
			}
		}
	}
	return rgba, nil
}

// Decode reads a BMP image from r and returns it as an image.Image.
func Decode(r io.Reader) (image.Image, error) {
	d := &decoder{r: r}
	if err := d.DecodeConfig(); err != nil {
		return nil, err
	}
	return d.Decode()
}

// DecodeConfig returns the color model and dimensions of a BMP image without
// decoding the entire image.
func DecodeConfig(r io.Reader) (image.Config, error) {
	d := &decoder{r: r}
	if err := d.DecodeConfig(); err != nil {
		return image.Config{}, err
	}
	return d.c, nil
}

func init() {
	image.RegisterFormat("bmp", "BM????\x00\x00\x00\x00", Decode, DecodeConfig)
}
