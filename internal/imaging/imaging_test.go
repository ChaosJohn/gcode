// Ported: jcode-base image_normalize tests (plus ICO/BMP decode).
package imaging

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"testing"

	"golang.org/x/image/bmp"
)

func tinyBMP(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{R: 255})
	var buf bytes.Buffer
	if err := bmp.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func tinyPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// Ported: provider_formats_pass_through_untouched.
func TestProviderFormatsPassThrough(t *testing.T) {
	original := tinyPNG(t)
	out, err := NormalizeImageBytes(original)
	if err != nil {
		t.Fatal(err)
	}
	if out.MediaType != "image/png" || !bytes.Equal(out.Data, original) || out.ConvertedFrom != "" {
		t.Fatalf("png passthrough: %+v", out)
	}
}

// Ported: bmp_ico_and_tiff_are_converted_to_png.
func TestConvertsToPNG(t *testing.T) {
	out, err := NormalizeImageBytes(tinyBMP(t))
	if err != nil {
		t.Fatal(err)
	}
	if out.MediaType != "image/png" || out.ConvertedFrom != "BMP" {
		t.Fatalf("bmp conversion: %+v", out)
	}
	if SniffProviderMediaType(out.Data) != "image/png" {
		t.Fatalf("converted bytes must sniff as png")
	}
}

// Ported: undecodable_formats_are_refused_with_a_reason.
func TestRefusesWithReason(t *testing.T) {
	heic := []byte{0, 0, 0, 24}
	heic = append(heic, []byte("ftypheic")...)
	heic = append(heic, make([]byte, 16)...)
	if _, err := NormalizeImageBytes(heic); err == nil || !contains(err.Error(), "HEIC") {
		t.Fatalf("heic: %v", err)
	}
	if _, err := NormalizeImageBytes([]byte("<svg xmlns='x'></svg>")); err == nil || !contains(err.Error(), "SVG") {
		t.Fatalf("svg: %v", err)
	}
	if _, err := NormalizeImageBytes([]byte("definitely not an image")); err == nil || !contains(err.Error(), "unrecognised") {
		t.Fatalf("unknown: %v", err)
	}
	if _, err := NormalizeImageBytes([]byte("BMgarbage")); err == nil || !contains(err.Error(), "BMP") {
		t.Fatalf("corrupt bmp: %v", err)
	}
}

func TestImageDimensions(t *testing.T) {
	data := tinyPNG(t)
	w, h, ok := ImageDimensions(data)
	if !ok || w != 2 || h != 2 {
		t.Fatalf("png dims: %d %d %v", w, h, ok)
	}
	// A synthetic BMP header (54-byte offset, 3x2).
	header := make([]byte, 54)
	copy(header, "BM")
	binary.LittleEndian.PutUint32(header[18:], 3)
	binary.LittleEndian.PutUint32(header[22:], 2)
	w, h, ok = ImageDimensions(header)
	if !ok || w != 3 || h != 2 {
		t.Fatalf("bmp dims: %d %d %v", w, h, ok)
	}
}
