package platform

import (
	"bytes"
	_ "embed"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/png"
)

// trayIconPNG is a copy of packaging/icon.png (a test keeps them equal).
//
//go:embed trayicon.png
var trayIconPNG []byte

// scaleIcon resizes src to size×size by averaging the source pixels each
// target pixel covers (with alpha weighting, so transparent edges don't
// darken). Good enough for shrinking a square icon.
func scaleIcon(src image.Image, size int) *image.NRGBA {
	b := src.Bounds()
	dst := image.NewNRGBA(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		y0, y1 := b.Min.Y+y*b.Dy()/size, b.Min.Y+(y+1)*b.Dy()/size
		for x := 0; x < size; x++ {
			x0, x1 := b.Min.X+x*b.Dx()/size, b.Min.X+(x+1)*b.Dx()/size
			var r, g, bl, a, n uint64
			for sy := y0; sy < max(y1, y0+1); sy++ {
				for sx := x0; sx < max(x1, x0+1); sx++ {
					c := color.NRGBA64Model.Convert(src.At(sx, sy)).(color.NRGBA64)
					r += uint64(c.R) * uint64(c.A)
					g += uint64(c.G) * uint64(c.A)
					bl += uint64(c.B) * uint64(c.A)
					a += uint64(c.A)
					n++
				}
			}
			if a == 0 {
				continue
			}
			dst.SetNRGBA(x, y, color.NRGBA{
				R: uint8(r / a >> 8), G: uint8(g / a >> 8), B: uint8(bl / a >> 8),
				A: uint8(a / n >> 8),
			})
		}
	}
	return dst
}

func decodeTrayIcon() (image.Image, error) {
	img, err := png.Decode(bytes.NewReader(trayIconPNG))
	if err != nil {
		return nil, fmt.Errorf("decode tray icon: %w", err)
	}
	return img, nil
}

// trayIconPNGSized returns the icon as a size×size PNG (for Linux trays).
func trayIconPNGSized(size int) ([]byte, error) {
	img, err := decodeTrayIcon()
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, scaleIcon(img, size)); err != nil {
		return nil, fmt.Errorf("encode tray icon: %w", err)
	}
	return buf.Bytes(), nil
}

// trayIconICO returns the icon as a Windows .ico file with one 32-bit
// bitmap image per size (the format every Windows version loads).
func trayIconICO(sizes ...int) ([]byte, error) {
	img, err := decodeTrayIcon()
	if err != nil {
		return nil, err
	}
	images := make([][]byte, len(sizes))
	for i, size := range sizes {
		images[i] = icoBitmap(scaleIcon(img, size))
	}
	var buf bytes.Buffer
	le := binary.LittleEndian
	// ICONDIR, then one ICONDIRENTRY per image, then the images.
	buf.Write(le.AppendUint16(nil, 0))
	buf.Write(le.AppendUint16(nil, 1)) // type: icon
	buf.Write(le.AppendUint16(nil, uint16(len(sizes))))
	offset := 6 + 16*len(sizes)
	for i, size := range sizes {
		dim := byte(size)
		if size >= 256 {
			dim = 0 // 0 means 256
		}
		buf.Write([]byte{dim, dim, 0, 0})
		buf.Write(le.AppendUint16(nil, 1))  // colour planes
		buf.Write(le.AppendUint16(nil, 32)) // bits per pixel
		buf.Write(le.AppendUint32(nil, uint32(len(images[i]))))
		buf.Write(le.AppendUint32(nil, uint32(offset)))
		offset += len(images[i])
	}
	for _, im := range images {
		buf.Write(im)
	}
	return buf.Bytes(), nil
}

// icoBitmap encodes img as an icon image: a BITMAPINFOHEADER (with the
// height doubled, as icons require), bottom-up BGRA pixels, and an all-zero
// AND mask (the alpha channel already says what is transparent).
func icoBitmap(img *image.NRGBA) []byte {
	size := img.Bounds().Dx()
	le := binary.LittleEndian
	var buf bytes.Buffer
	buf.Write(le.AppendUint32(nil, 40)) // header size
	buf.Write(le.AppendUint32(nil, uint32(size)))
	buf.Write(le.AppendUint32(nil, uint32(2*size)))
	buf.Write(le.AppendUint16(nil, 1))  // planes
	buf.Write(le.AppendUint16(nil, 32)) // bits per pixel
	buf.Write(make([]byte, 24))         // no compression; sizes and colours unused
	for y := size - 1; y >= 0; y-- {
		for x := 0; x < size; x++ {
			c := img.NRGBAAt(x, y)
			buf.Write([]byte{c.B, c.G, c.R, c.A})
		}
	}
	maskRow := (size + 31) / 32 * 4 // 1 bit per pixel, rows padded to 4 bytes
	buf.Write(make([]byte, maskRow*size))
	return buf.Bytes()
}
