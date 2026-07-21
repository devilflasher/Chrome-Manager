package icondir

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"io"
	"sort"

	"github.com/sergeymakinen/go-bmp"
)

const (
	icoPrefix = "\x00\x00\x01\x00"
	curPrefix = "\x00\x00\x02\x00"

	pngPrefix = "\x89PNG\r\n\x1a\n"
)

const (
	fileHeaderLen = 6
	dirEntryLen   = 16

	bmpFileHeaderLen = 14
)

type FormatError string

func (e FormatError) Error() string { return string(e) }

type UnsupportedError string

func (e UnsupportedError) Error() string { return string(e) }

var maskPalette = color.Palette{
	color.Transparent,
	color.Opaque,
}

type reader struct {
	r               io.Reader
	pos             int64
	canSeekBackward *bool
}

func (r *reader) Read(p []byte) (n int, err error) {
	n, err = r.r.Read(p)
	r.pos += int64(n)
	return
}

func (r *reader) Seek(offset int64, whence int) (int64, error) {
	if r.CanSeekBackward() {
		n, err := r.r.(io.Seeker).Seek(offset, whence)
		if err != nil {
			return r.pos, err
		}
		r.pos = n
		return r.pos, err
	}
	if whence == io.SeekCurrent {
		offset, whence = r.pos+offset, io.SeekStart
	}
	if offset < r.pos || whence != io.SeekStart {
		return r.pos, UnsupportedError("overlapping offset")
	}
	if offset == r.pos {
		return r.pos, nil
	}
	n, err := io.CopyN(io.Discard, r.r, offset-r.pos)
	r.pos += n
	return r.pos, err
}

func (r *reader) CanSeekBackward() bool {
	if r.canSeekBackward != nil {
		return *r.canSeekBackward
	}
	r.canSeekBackward = new(bool)
	if s, ok := r.r.(io.Seeker); ok {
		// As mentioned in archive/tar, io.Seeker doesn't guarantee
		// seeking is actually supported until it's verified.
		if _, err := s.Seek(0, io.SeekCurrent); err == nil {
			*r.canSeekBackward = true
			return true
		}
	}
	*r.canSeekBackward = false
	return false
}

type Entry struct {
	Width, Height, Colors, BPP, XHotspot, YHotspot int
	Offset, Size                                   int64

	data, bmpHeader []byte
	topDown         bool
}

type Decoder struct {
	r       *reader
	icon    bool
	entries []*Entry
}

func NewDecoder(r io.Reader, icon bool) *Decoder {
	return &Decoder{
		r:    &reader{r: r},
		icon: icon,
	}
}

func (d *Decoder) DecodeDir() error {
	var b [16]byte
	if _, err := io.ReadFull(d.r, b[:fileHeaderLen]); err != nil {
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
		return err
	}
	if d.icon && string(b[:4]) != icoPrefix {
		return FormatError("not an ICO file")
	}
	if !d.icon && string(b[:4]) != curPrefix {
		return FormatError("not a CUR file")
	}
	count := binary.LittleEndian.Uint16(b[4:])
	if count == 0 {
		if d.icon {
			return FormatError("no icons")
		}
		return FormatError("no cursors")
	}
	for i := uint16(0); i < count; i++ {
		if _, err := io.ReadFull(d.r, b[:]); err != nil {
			if err == io.EOF {
				err = io.ErrUnexpectedEOF
			}
			return err
		}
		var xHotspot, yHotspot int
		if !d.icon {
			xHotspot, yHotspot = int(binary.LittleEndian.Uint16(b[4:])), int(binary.LittleEndian.Uint16(b[6:]))
		}
		size, offset := binary.LittleEndian.Uint32(b[8:]), binary.LittleEndian.Uint32(b[12:])
		d.entries = append(d.entries, &Entry{
			XHotspot: xHotspot,
			YHotspot: yHotspot,
			Offset:   int64(offset),
			Size:     int64(size),
		})
	}
	return d.readHeaders()
}

func (d *Decoder) Best() (*Entry, error) {
	var best *Entry
	for _, e := range d.entries {
		if best == nil || e.Width*e.Height > best.Width*best.Height || e.BPP > best.BPP {
			best = e
		}
	}
	return best, nil
}

func (d *Decoder) DecodeAll() ([]*Entry, []image.Image, error) {
	mm := make([]image.Image, len(d.entries))
	var err error
	for i, e := range d.entries {
		mm[i], err = d.Decode(e)
		if err != nil {
			return nil, nil, err
		}
	}
	return d.entries, mm, nil
}

func (d *Decoder) Decode(e *Entry) (image.Image, error) {
	r, isPNG, err := d.reader(e)
	if err != nil {
		return nil, err
	}
	if isPNG {
		return png.Decode(r)
	}
	m, err := bmp.Decode(r)
	if err != nil {
		return nil, err
	}
	mask, opaque, err := decodeMask(r, e)
	if err != nil {
		return nil, err
	}
	if !opaque {
		var transparent color.Color
		if paletted, ok := m.(*image.Paletted); ok {
			for _, c := range paletted.Palette {
				if _, _, _, a := c.RGBA(); a == 0 {
					transparent = c
					break
				}
			}
			if transparent == nil {
				if len(paletted.Palette) >= 256 {
					transparent = color.Transparent
					// The palette is already at its maximum capacity.
					tmp := image.NewRGBA(m.Bounds())
					draw.Draw(tmp, tmp.Bounds(), m, m.Bounds().Min, draw.Src)
					m = tmp
				} else {
					transparent = color.Transparent
					paletted.Palette = append(paletted.Palette, transparent)
				}
			}
		} else {
			transparent = color.Transparent
		}
		dst := m.(draw.Image)
		for x := 0; x < e.Width; x++ {
			for y := 0; y < e.Height; y++ {
				if mask.At(x, y) == color.Opaque {
					dst.Set(x, y, transparent)
				}
			}
		}
	}
	return m, nil
}

func (d *Decoder) DecodeConfig(e *Entry) (image.Config, error) {
	r, isPNG, err := d.reader(e)
	if err != nil {
		return image.Config{}, err
	}
	if isPNG {
		return png.DecodeConfig(r)
	}
	return bmp.DecodeConfig(r)
}

func (d *Decoder) readHeaders() error {
	if !d.r.CanSeekBackward() {
		return d.readData()
	}
	for _, e := range d.entries {
		if _, err := d.r.Seek(e.Offset, io.SeekStart); err != nil {
			return err
		}
		if err := decodeHeader(d.r, e); err != nil {
			return err
		}
	}
	return nil
}

func (d *Decoder) readData() error {
	entries := append([]*Entry{}, d.entries...)
	sort.Slice(entries, func(i, j int) bool { return entries[i].Offset < entries[j].Offset })
	for _, e := range entries {
		if _, err := d.r.Seek(e.Offset, io.SeekStart); err != nil {
			return err
		}
		e.data = make([]byte, e.Size)
		if _, err := io.ReadFull(d.r, e.data); err != nil {
			return err
		}
		if err := decodeHeader(bytes.NewReader(e.data), e); err != nil {
			return err
		}
	}
	return nil
}

func (d *Decoder) reader(e *Entry) (r io.Reader, isPNG bool, err error) {
	if d.r.CanSeekBackward() {
		if _, err = d.r.Seek(e.Offset, io.SeekStart); err != nil {
			return
		}
		r = d.r
	} else {
		r = bytes.NewReader(e.data)
	}
	if isPNG = e.bmpHeader == nil; !isPNG {
		r = io.MultiReader(bytes.NewReader(e.bmpHeader), r)
	}
	return
}

type peekReader interface {
	io.Reader
	Peek(int) ([]byte, error)
}

func decodeHeader(r io.Reader, e *Entry) error {
	var rd peekReader
	if pr, ok := r.(peekReader); ok {
		rd = pr
	} else {
		rd = bufio.NewReader(r)
	}
	b, err := rd.Peek(len(pngPrefix))
	if err != nil {
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
		return err
	}
	if string(b) == pngPrefix {
		if err = decodePNGHeader(rd, e); err != nil {
			return err
		}
	} else {
		if err = decodeBMPHeader(rd, e); err != nil {
			return err
		}
	}
	return nil
}

func decodePNGHeader(r io.Reader, e *Entry) error {
	const ihdrLength = 13
	const (
		ctGrayscale      = 0
		ctTrueColor      = 2
		ctPaletted       = 3
		ctGrayscaleAlpha = 4
		ctTrueColorAlpha = 6
	)
	if _, err := io.CopyN(io.Discard, r, int64(len(pngPrefix))); err != nil {
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
		return err
	}
	var b [ihdrLength + 4]byte
	seekChunk := func(typ string) (int, error) {
		for {
			if _, err := io.ReadFull(r, b[:8]); err != nil {
				if err == io.EOF {
					err = io.ErrUnexpectedEOF
				}
				return 0, err
			}
			n := binary.BigEndian.Uint32(b[:4])
			if string(b[4:8]) == typ {
				return int(n), nil
			}
			if _, err := io.CopyN(io.Discard, r, int64(n)+4); err != nil {
				if err == io.EOF {
					return -1, nil
				}
				return 0, err
			}
		}
	}
	length, err := seekChunk("IHDR")
	if err != nil {
		return err
	}
	if length != ihdrLength {
		return UnsupportedError("PNG image")
	}
	if _, err = io.ReadFull(r, b[:]); err != nil {
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
		return err
	}
	e.Width, e.Height = int(binary.BigEndian.Uint32(b[:])), int(binary.BigEndian.Uint32(b[4:]))
	paletted := false
	switch b[8] {
	case 1:
		switch b[9] {
		case ctPaletted:
			paletted = true
			fallthrough
		case ctGrayscale:
			e.BPP = 1
		}
	case 2:
		switch b[9] {
		case ctPaletted:
			paletted = true
			fallthrough
		case ctGrayscale:
			e.BPP = 2
		}
	case 4:
		switch b[9] {
		case ctPaletted:
			paletted = true
			fallthrough
		case ctGrayscale:
			e.BPP = 4
		}
	case 8:
		switch b[9] {
		case ctPaletted:
			paletted = true
			fallthrough
		case ctGrayscale:
			e.BPP = 8
		case ctTrueColor:
			e.BPP = 24
		case ctGrayscaleAlpha:
			e.BPP = 16
		case ctTrueColorAlpha:
			e.BPP = 32
		}
	case 16:
		switch b[9] {
		case ctGrayscale:
			e.BPP = 16
		case ctTrueColor:
			e.BPP = 48
		case ctGrayscaleAlpha:
			e.BPP = 32
		case ctTrueColorAlpha:
			e.BPP = 64
		}
	}
	if e.BPP == 0 {
		return UnsupportedError("PNG image")
	}
	if paletted {
		length, err = seekChunk("PLTE")
		if err != nil {
			return err
		}
		if length != -1 {
			e.Colors = length / 3
		}
	}
	return nil
}

func decodeBMPHeader(r io.Reader, e *Entry) error {
	const infoHeaderLen = 40
	const biBitFields = 3
	e.bmpHeader = make([]byte, bmpFileHeaderLen+infoHeaderLen)
	if _, err := io.ReadFull(r, e.bmpHeader[bmpFileHeaderLen:]); err != nil {
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
		return err
	}
	infoLen := binary.LittleEndian.Uint32(e.bmpHeader[14:])
	if infoLen < infoHeaderLen {
		return UnsupportedError("BMP image")
	}
	e.bmpHeader[0], e.bmpHeader[1] = 'B', 'M'
	e.Width, e.Height, e.BPP = int(int32(binary.LittleEndian.Uint32(e.bmpHeader[18:]))), int(int32(binary.LittleEndian.Uint32(e.bmpHeader[22:]))), int(binary.LittleEndian.Uint16(e.bmpHeader[28:]))
	if e.Height < 0 {
		e.Height, e.topDown = -e.Height, true
	}
	if e.Height%2 != 0 {
		return UnsupportedError("BMP image")
	}
	e.Height /= 2
	compression := binary.LittleEndian.Uint32(e.bmpHeader[30:])
	e.Colors = int(binary.LittleEndian.Uint32(e.bmpHeader[46:]))
	if e.BPP <= 8 && e.Colors == 0 {
		e.Colors = 1 << e.BPP
	}
	colorMaskLen := uint32(0)
	if infoLen == infoHeaderLen && (e.BPP == 16 || e.BPP == 32) && compression == biBitFields {
		colorMaskLen = 4 * 3
	}
	binary.LittleEndian.PutUint32(e.bmpHeader[10:], bmpFileHeaderLen+infoLen+uint32(e.Colors)*4+colorMaskLen)
	// Fix height.
	binary.LittleEndian.PutUint32(e.bmpHeader[22:], uint32(e.Height))
	e.Offset, e.Size = e.Offset+infoHeaderLen, e.Size-infoHeaderLen
	if e.data != nil {
		e.data = e.data[infoHeaderLen:]
	}
	return nil
}

func decodeMask(r io.Reader, e *Entry) (m *image.Paletted, opaque bool, err error) {
	paletted := image.NewPaletted(image.Rect(0, 0, e.Width, e.Height), maskPalette)
	if e.Width == 0 || e.Height == 0 {
		return paletted, true, nil
	}
	// There is 1 bit per pixel, and each row is 4-byte aligned.
	b := make([]byte, ((e.Width+8-1)/8+3)&^3)
	y0, y1, yDelta := e.Height-1, -1, -1
	if e.topDown {
		y0, y1, yDelta = 0, e.Height, +1
	}
	opaque = true
	for y := y0; y != y1; y += yDelta {
		p := paletted.Pix[y*paletted.Stride : y*paletted.Stride+e.Width]
		if _, err := io.ReadFull(r, b); err != nil {
			return nil, false, err
		}
		byte, bit := 0, 7
		for x := 0; x < e.Width; x++ {
			p[x] = (b[byte] >> bit) & 1
			if p[x] != 0 {
				opaque = false
			}
			if bit == 0 {
				bit = 7
				byte++
			} else {
				bit--
			}
		}
	}
	return paletted, opaque, nil
}
