package icondir

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"io"
	"strconv"

	"github.com/sergeymakinen/go-bmp"
)

type Encoder struct {
	w       io.Writer
	icon    bool
	entries []*Entry
}

func NewEncoder(w io.Writer, icon bool) *Encoder {
	return &Encoder{
		w:    w,
		icon: icon,
	}
}

func (e *Encoder) Add(m image.Image, xHotspot, yHotspot int) error {
	d := m.Bounds().Size()
	if d.X < 1 || d.Y < 1 || d.X > 256 || d.Y > 256 {
		return FormatError("invalid image size: " + strconv.Itoa(d.X) + "x" + strconv.Itoa(d.Y))
	}
	if m, ok := m.(*image.Paletted); ok && (len(m.Palette) == 0 || len(m.Palette) > 256) {
		return FormatError("bad palette length: " + strconv.Itoa(len(m.Palette)))
	}
	if xHotspot < 0 || yHotspot < 0 || xHotspot > 256 || yHotspot > 256 {
		return FormatError("invalid hotspot: " + strconv.Itoa(xHotspot) + "x" + strconv.Itoa(yHotspot))
	}
	entry := &Entry{
		Width:    d.X,
		Height:   d.Y,
		XHotspot: xHotspot,
		YHotspot: yHotspot,
	}
	isPNG := false
	if d.X == 256 && d.Y == 256 {
		switch m := m.(type) {
		case *image.Paletted:
			entry.Colors = len(m.Palette)
		case *image.Gray:
			entry.Colors = 256
		default:
			isPNG = true
		}
	}
	m2 := m
	var buf bytes.Buffer
	if isPNG {
		// Icon's PNGs are always expected to be 32 bit.
		if rgba, ok := m.(*image.RGBA); ok {
			m2 = &nonOpaqueRGBA{rgba}
		} else {
			tmp := image.NewRGBA(m.Bounds())
			draw.Draw(tmp, tmp.Bounds(), m, m.Bounds().Min, draw.Src)
			m2 = &nonOpaqueRGBA{tmp}
		}
		if err := png.Encode(&buf, m2); err != nil {
			return err
		}
		entry.data = buf.Bytes()
	} else {
		if paletted, ok := m.(*image.Paletted); ok {
			// Remove transparent color from palette.
			var p color.Palette
			for _, c := range paletted.Palette {
				if _, _, _, a := c.RGBA(); a != 0 {
					p = append(p, c)
				}
			}
			redraw := len(p) != len(paletted.Palette)
			if n := len(p); n == 3 || n == 4 {
				// 2 BPP images are not supported.
				p = append(p, []color.Color{color.Black, color.Black}[:5-n]...)
				redraw = true
			}
			if redraw {
				tmp := image.NewPaletted(m.Bounds(), p)
				drawOpaque(tmp, m)
				m2 = tmp
				entry.Colors = len(tmp.Palette)
			}
		} else if _, ok := m.(*image.Gray); !ok {
			if opaque, semiopaque := opaque(m); !opaque && !semiopaque {
				tmp := image.NewRGBA(m.Bounds())
				draw.Draw(tmp, tmp.Bounds(), image.Black, image.Point{}, draw.Src)
				draw.Draw(tmp, tmp.Bounds(), m, m.Bounds().Min, draw.Over)
				m2 = tmp
			}
		}
		if err := bmp.Encode(&buf, m2); err != nil {
			return err
		}
		entry.bmpHeader = buf.Bytes()[:bmpFileHeaderLen]
		n, err := encodeMask(&buf, m)
		if err != nil {
			return err
		}
		entry.data = buf.Bytes()[bmpFileHeaderLen:]
		// Fix height.
		binary.LittleEndian.PutUint32(entry.data[8:], uint32(entry.Height*2))
		// Add mask size to image size.
		size := binary.LittleEndian.Uint32(entry.data[20:])
		binary.LittleEndian.PutUint32(entry.data[20:], size+uint32(n))
	}
	tmp := &Entry{}
	if err := decodeHeader(bytes.NewReader(entry.data), tmp); err != nil {
		return err
	}
	entry.Colors, entry.BPP, entry.Size = tmp.Colors, tmp.BPP, int64(len(entry.data))
	e.entries = append(e.entries, entry)
	return nil
}

func (e *Encoder) Encode() error {
	h := struct {
		prefix [4]byte
		count  uint16
	}{count: uint16(len(e.entries))}
	if e.icon {
		copy(h.prefix[:], icoPrefix)
	} else {
		copy(h.prefix[:], curPrefix)
	}
	if err := binary.Write(e.w, binary.LittleEndian, h); err != nil {
		return err
	}
	trunc := func(n int) byte {
		if n >= 256 {
			n = 0
		}
		return byte(n)
	}
	off := fileHeaderLen + dirEntryLen*len(e.entries)
	for _, m := range e.entries {
		d := struct {
			width                byte
			height               byte
			colorUse             byte
			reserved             byte
			colorPlaneOrXHotspot uint16
			bppOrYHotspot        uint16
			imageSize            uint32
			imageOffset          uint32
		}{
			width:       trunc(m.Width),
			height:      trunc(m.Height),
			colorUse:    trunc(m.Colors),
			imageSize:   uint32(len(m.data)),
			imageOffset: uint32(off),
		}
		if e.icon {
			d.colorPlaneOrXHotspot = 1
			d.bppOrYHotspot = uint16(m.BPP)
		} else {
			d.colorPlaneOrXHotspot = uint16(m.XHotspot)
			d.bppOrYHotspot = uint16(m.YHotspot)
		}
		off += len(m.data)
		if err := binary.Write(e.w, binary.LittleEndian, d); err != nil {
			return err
		}
	}
	for _, entry := range e.entries {
		if _, err := e.w.Write(entry.data); err != nil {
			return err
		}
	}
	return nil
}

func encodeMask(w io.Writer, m image.Image) (n int, err error) {
	d := m.Bounds().Size()
	mask := image.NewPaletted(m.Bounds(), maskPalette)
	for y := m.Bounds().Min.Y; y < m.Bounds().Max.Y; y++ {
		for x := m.Bounds().Min.X; x < m.Bounds().Max.X; x++ {
			if _, _, _, a := m.At(x, y).RGBA(); a == 0 {
				mask.Set(x, y, color.Opaque)
			}
		}
	}
	// There is 1 bit per pixel, and each row is 4-byte aligned.
	b := make([]byte, ((d.X+8-1)/8+3)&^3)
	for y := d.Y - 1; y >= 0; y-- {
		byte, bit := 0, 7
		for x := 0; x < d.X; x++ {
			b[byte] = (b[byte] & ^(1 << bit)) | (mask.Pix[y*mask.Stride+x] << bit)
			if bit == 0 {
				bit = 7
				byte++
			} else {
				bit--
			}
		}
		if n2, err2 := w.Write(b); err2 != nil {
			err = err2
			return
		} else {
			n += n2
		}
	}
	return
}

func drawOpaque(dst draw.Image, src image.Image) {
	for y := src.Bounds().Min.Y; y < src.Bounds().Max.Y; y++ {
		for x := src.Bounds().Min.X; x < src.Bounds().Max.X; x++ {
			c := src.At(x, y)
			if _, _, _, a := src.At(x, y).RGBA(); a != 0 {
				dst.Set(x, y, c)
			}
		}
	}
}

type opaquer interface {
	Opaque() bool
}

func opaque(m image.Image) (opaque, semiopaque bool) {
	if o, ok := m.(opaquer); ok && o.Opaque() {
		return true, false
	}
	opaque = true
	for y := m.Bounds().Min.Y; y < m.Bounds().Max.Y; y++ {
		for x := m.Bounds().Min.X; x < m.Bounds().Max.X; x++ {
			if _, _, _, a := m.At(x, y).RGBA(); a != 0 {
				opaque = false
				if a != 0xFFFF {
					semiopaque = true
					break
				}
			}
		}
	}
	return
}

type nonOpaqueRGBA struct {
	*image.RGBA
}

func (*nonOpaqueRGBA) Opaque() bool { return false }
