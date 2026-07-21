# bmp

[![tests](https://github.com/sergeymakinen/go-bmp/workflows/tests/badge.svg)](https://github.com/sergeymakinen/go-bmp/actions?query=workflow%3Atests)
[![Go Reference](https://pkg.go.dev/badge/github.com/sergeymakinen/go-bmp.svg)](https://pkg.go.dev/github.com/sergeymakinen/go-bmp)
[![Go Report Card](https://goreportcard.com/badge/github.com/sergeymakinen/go-bmp)](https://goreportcard.com/report/github.com/sergeymakinen/go-bmp)
[![codecov](https://codecov.io/gh/sergeymakinen/go-bmp/branch/main/graph/badge.svg)](https://codecov.io/gh/sergeymakinen/go-bmp)

Package bmp implements a BMP image decoder and encoder.

The BMP specification is at http://www.digicamsoft.com/bmp/bmp.html.

## Supported BMP features

* 1, 2, 4, 8, 16, 24 and 32 bits per pixel
* Top-down images (read-only)
* RLE compression for 4 and 8 BPP images (read-only)
* RGB555 and RGB565 types for 16 BPP images (read-only)

## Installation

Use go get:

```bash
go get github.com/sergeymakinen/go-bmp
```

Then import the package into your own code:

```go
import "github.com/sergeymakinen/go-bmp"
```


## Example

```go
f, _ := os.Open("file.bmp")
img, _ := bmp.Decode(f)
for i := 10; i < 20; i++ {
    img.(draw.Image).Set(i, i, color.NRGBA{
        R: 255,
        G: 0,
        B: 0,
        A: 255,
    })
}
f.Truncate(0)
bmp.Encode(f, img)
f.Close()
```

## License

BSD 3-Clause
