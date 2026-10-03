package platform

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

func TestTrayIconMatchesPackagingIcon(t *testing.T) {
	want, err := os.ReadFile("../../packaging/icon.png")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(trayIconPNG, want) {
		t.Fatal("internal/platform/trayicon.png differs from packaging/icon.png; copy it again")
	}
}

func TestTrayIconICO(t *testing.T) {
	sizes := []int{16, 32, 48, 256}
	ico, err := trayIconICO(sizes...)
	if err != nil {
		t.Fatal(err)
	}
	le := binary.LittleEndian
	if le.Uint16(ico[0:]) != 0 || le.Uint16(ico[2:]) != 1 || int(le.Uint16(ico[4:])) != len(sizes) {
		t.Fatalf("bad ICONDIR % x", ico[:6])
	}
	for i, size := range sizes {
		e := ico[6+16*i:]
		dim := int(e[0])
		if dim == 0 {
			dim = 256
		}
		n, off := int(le.Uint32(e[8:])), int(le.Uint32(e[12:]))
		if dim != size || off+n > len(ico) {
			t.Fatalf("entry %d: size %d, offset %d, length %d (file %d)", i, dim, off, n, len(ico))
		}
		bmp := ico[off : off+n]
		mask := (size + 31) / 32 * 4 * size
		if le.Uint32(bmp) != 40 || int(le.Uint32(bmp[4:])) != size || int(le.Uint32(bmp[8:])) != 2*size ||
			le.Uint16(bmp[14:]) != 32 || n != 40+4*size*size+mask {
			t.Fatalf("entry %d: bad bitmap header or length %d", i, n)
		}
	}
}

func TestTrayIconPNGSized(t *testing.T) {
	b, err := trayIconPNGSized(64)
	if err != nil {
		t.Fatal(err)
	}
	img, _, err := image.Decode(bytes.NewReader(b))
	if err != nil || img.Bounds().Dx() != 64 || img.Bounds().Dy() != 64 {
		t.Fatalf("got %v, %v", img.Bounds(), err)
	}
}

func TestScaleIcon(t *testing.T) {
	// Left half opaque red, right half transparent but one red pixel:
	// transparent pixels lower the alpha but don't darken the colour.
	src := image.NewNRGBA(image.Rect(0, 0, 4, 4))
	for y := 0; y < 4; y++ {
		for x := 0; x < 2; x++ {
			src.SetNRGBA(x, y, color.NRGBA{R: 255, A: 255})
		}
	}
	src.SetNRGBA(3, 0, color.NRGBA{R: 255, A: 255})
	got := scaleIcon(src, 2)
	if c := got.NRGBAAt(0, 0); c != (color.NRGBA{R: 255, A: 255}) {
		t.Fatalf("left: %v", c)
	}
	if c := got.NRGBAAt(1, 0); c.R != 255 || c.A != 63 {
		t.Fatalf("top right (1 of 4 pixels red): %v", c)
	}
	if c := got.NRGBAAt(1, 1); c.A != 0 {
		t.Fatalf("bottom right: %v", c)
	}
}

func TestDebounce(t *testing.T) {
	var n atomic.Int32
	f := debounce(time.Hour, func() { n.Add(1) })
	f()
	f()
	if n.Load() != 1 {
		t.Fatalf("called %d times", n.Load())
	}
	g := debounce(0, func() { n.Add(1) })
	g()
	g()
	if n.Load() != 3 {
		t.Fatalf("called %d times", n.Load())
	}
}

// Without a tray, Loop waits for Stop whether or not Show was called.
func TestTrayWithoutTray(t *testing.T) {
	old := haveTray
	haveTray = func() bool { return false }
	t.Cleanup(func() { haveTray = old })

	for _, show := range []bool{false, true} {
		tr := NewTray()
		done := make(chan struct{})
		go func() { tr.Loop(); close(done) }()
		if show {
			tr.Show(TrayMenu{Tooltip: "TunnelTab"})
			tr.Show(TrayMenu{Tooltip: "ignored"}) // must not block
		}
		select {
		case <-done:
			t.Fatal("Loop returned before Stop")
		case <-time.After(50 * time.Millisecond):
		}
		if tr.Shown() {
			t.Fatal("Shown without a tray")
		}
		tr.Stop()
		tr.Stop() // twice is fine
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("Loop didn't return after Stop")
		}
	}
}
