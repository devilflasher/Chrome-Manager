package ico

import (
	"image"
	"io"

	"github.com/sergeymakinen/go-ico/internal/icondir"
)

// EncodeAll writes the icons in mm to w in ICO format.
func EncodeAll(w io.Writer, mm []image.Image) error {
	e := icondir.NewEncoder(w, true)
	for _, m := range mm {
		if err := e.Add(m, 0, 0); err != nil {
			return convertErr(err)
		}
	}
	return convertErr(e.Encode())
}

// Encode writes the icon m to w in ICO format.
func Encode(w io.Writer, m image.Image) error {
	e := icondir.NewEncoder(w, true)
	if err := e.Add(m, 0, 0); err != nil {
		return convertErr(err)
	}
	return convertErr(e.Encode())
}
