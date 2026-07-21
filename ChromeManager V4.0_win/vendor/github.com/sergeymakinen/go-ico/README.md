# ico

[![tests](https://github.com/sergeymakinen/go-ico/workflows/tests/badge.svg)](https://github.com/sergeymakinen/go-ico/actions?query=workflow%3Atests)
[![Go Reference](https://pkg.go.dev/badge/github.com/sergeymakinen/go-ico.svg)](https://pkg.go.dev/github.com/sergeymakinen/go-ico)
[![Go Report Card](https://goreportcard.com/badge/github.com/sergeymakinen/go-ico)](https://goreportcard.com/report/github.com/sergeymakinen/go-ico)
[![codecov](https://codecov.io/gh/sergeymakinen/go-ico/branch/main/graph/badge.svg)](https://codecov.io/gh/sergeymakinen/go-ico)

Package ico implements an ICO file decoder and encoder.
Package cur implements a CUR file decoder and encoder.

See https://en.wikipedia.org/wiki/ICO_(file_format) for more information.

## Installation

Use go get:

```bash
go get github.com/sergeymakinen/go-ico
```

Then import the package into your own code:

```go
import "github.com/sergeymakinen/go-ico"
```

## Example

```go
b, _ := os.ReadFile("icon_32x32-32.png")
m1, _ := png.Decode(bytes.NewReader(b))
b, _ = os.ReadFile("icon_256x256-32.png")
m2, _ := png.Decode(bytes.NewReader(b))
f, _ := os.Create("icon.ico")
ico.EncodeAll(f, []image.Image{m1, m2})
f.Close()
```

## License

BSD 3-Clause
