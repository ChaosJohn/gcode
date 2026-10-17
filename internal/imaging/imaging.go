// Package imaging ports jcode-base image_normalize: normalize image bytes
// into a format every vision provider accepts (png/jpeg/gif/webp).
package imaging

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/png"

	"golang.org/x/image/bmp"
	"golang.org/x/image/tiff"
)

// ProviderImageMediaTypes are the media types every vision-capable provider
// jcode talks to accepts.
var ProviderImageMediaTypes = []string{"image/png", "image/jpeg", "image/gif", "image/webp"}

// IsProviderMediaType reports whether the media type is provider-accepted.
func IsProviderMediaType(mediaType string) bool {
	for _, accepted := range ProviderImageMediaTypes {
		if equalFold(accepted, mediaType) {
			return true
		}
	}
	return false
}

// SniffProviderMediaType detects a provider-accepted media type from the
// leading magic bytes. Empty for every other format.
func SniffProviderMediaType(data []byte) string {
	if len(data) >= 8 && bytes.Equal(data[0:8], []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}) {
		return "image/png"
	}
	if len(data) >= 3 && data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF {
		return "image/jpeg"
	}
	if len(data) >= 6 && (bytes.Equal(data[0:6], []byte("GIF87a")) || bytes.Equal(data[0:6], []byte("GIF89a"))) {
		return "image/gif"
	}
	if len(data) >= 12 && bytes.Equal(data[0:4], []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WEBP")) {
		return "image/webp"
	}
	return ""
}

// sniffOtherFormat names a recognised non-provider image format, for
// conversion and for user-facing explanations.
func sniffOtherFormat(data []byte) string {
	if len(data) >= 2 && bytes.Equal(data[0:2], []byte("BM")) {
		return "BMP"
	}
	if len(data) >= 4 && bytes.Equal(data[0:4], []byte{0, 0, 1, 0}) {
		return "ICO"
	}
	if len(data) >= 4 && (bytes.Equal(data[0:4], []byte("II*\x00")) || bytes.Equal(data[0:4], []byte("MM\x00*"))) {
		return "TIFF"
	}
	if len(data) >= 12 && bytes.Equal(data[4:8], []byte("ftyp")) {
		brand := string(data[8:12])
		switch brand {
		case "avif", "avis":
			return "AVIF"
		case "heic", "heix", "heim", "heis", "hevc", "hevx", "mif1", "msf1":
			return "HEIC"
		}
	}
	head := data
	if len(head) > 256 {
		head = head[:256]
	}
	text := string(head)
	text = stringsTrimBOM(text)
	text = stringsTrimLeftSpace(text)
	if hasPrefix(text, "<svg") || (hasPrefix(text, "<?xml") && contains(text, "<svg")) {
		return "SVG"
	}
	return ""
}

// ProviderImage is an image ready to be attached to a provider request.
type ProviderImage struct {
	// MediaType is one of ProviderImageMediaTypes, matching Data.
	MediaType string
	Data      []byte
	// ConvertedFrom names the source format when the bytes were re-encoded
	// (for example "BMP").
	ConvertedFrom string
}

// NormalizeImageBytes turns arbitrary image bytes into a provider-safe image.
//   - PNG/JPEG/GIF/WebP pass through untouched, labelled by magic bytes.
//   - BMP/ICO/TIFF are decoded and re-encoded as PNG.
//   - Anything else (HEIC, AVIF, SVG, corrupt data) returns an error with a
//     short explanation suitable for showing to the model or the user.
func NormalizeImageBytes(data []byte) (ProviderImage, error) {
	if mediaType := SniffProviderMediaType(data); mediaType != "" {
		return ProviderImage{MediaType: mediaType, Data: data}, nil
	}
	switch format := sniffOtherFormat(data); format {
	case "BMP", "ICO", "TIFF":
		if pngData, ok := reencodePNG(format, data); ok {
			return ProviderImage{MediaType: "image/png", Data: pngData, ConvertedFrom: format}, nil
		}
		return ProviderImage{}, fmt.Errorf("%s data could not be decoded, so it cannot be sent to the model", format)
	case "":
		return ProviderImage{}, errors.New("unrecognised or corrupt image data, so it cannot be sent to the model")
	default:
		return ProviderImage{}, fmt.Errorf("%s is not a format the model can view directly; convert it to PNG or JPEG and read the result", format)
	}
}

func reencodePNG(format string, data []byte) ([]byte, bool) {
	var img image.Image
	var err error
	switch format {
	case "BMP":
		img, err = bmp.Decode(bytes.NewReader(data))
	case "TIFF":
		img, err = tiff.Decode(bytes.NewReader(data))
	case "ICO":
		img, err = decodeICO(data)
	default:
		return nil, false
	}
	if err != nil || img == nil {
		return nil, false
	}
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		return nil, false
	}
	return out.Bytes(), true
}

// decodeICO extracts an ICO's largest image entry: modern icons embed PNG
// bytes; legacy ones embed BMP data decodable by the BMP decoder.
func decodeICO(data []byte) (image.Image, error) {
	if len(data) < 6 {
		return nil, errors.New("ico too short")
	}
	count := int(data[4]) | int(data[5])<<8
	best := -1
	bestArea := 0
	for i := 0; i < count; i++ {
		offset := 6 + i*16
		if offset+16 > len(data) {
			break
		}
		width := int(data[offset])
		height := int(data[offset+1])
		if width == 0 {
			width = 256
		}
		if height == 0 {
			height = 256
		}
		size := int(uint32(data[offset+8]) | uint32(data[offset+9])<<8 | uint32(data[offset+10])<<16 | uint32(data[offset+11])<<24)
		start := int(uint32(data[offset+12]) | uint32(data[offset+13])<<8 | uint32(data[offset+14])<<16 | uint32(data[offset+15])<<24)
		if start+size > len(data) {
			continue
		}
		area := width * height
		if area > bestArea {
			bestArea = area
			best = i
			_ = start
			_ = size
		}
	}
	if best < 0 {
		return nil, errors.New("no ico entries")
	}
	offset := 6 + best*16
	size := int(uint32(data[offset+8]) | uint32(data[offset+9])<<8 | uint32(data[offset+10])<<16 | uint32(data[offset+11])<<24)
	start := int(uint32(data[offset+12]) | uint32(data[offset+13])<<8 | uint32(data[offset+14])<<16 | uint32(data[offset+15])<<24)
	if start+size > len(data) {
		return nil, errors.New("ico entry out of bounds")
	}
	entry := data[start : start+size]
	// Modern ICO entries carry a bare PNG; legacy ones a BITMAPINFOHEADER
	// (40-byte) BMP without the file header.
	if SniffProviderMediaType(entry) == "image/png" {
		img, err := png.Decode(bytes.NewReader(entry))
		if err == nil {
			return img, nil
		}
	}
	if len(entry) > 14 && entry[12] == 40 && entry[13] == 0 && entry[14] == 0 && entry[15] == 0 {
		// Rebuild a file header (14 bytes) around the info header: pixel
		// array offset = 14 + 40 + palette (bpp <= 8) — the BMP decoder can
		// accept the headerless variant only via the full file form, so
		// approximate the palette size from bpp.
		bpp := int(entry[14]) | int(entry[15])<<8
		palette := 0
		if bpp <= 8 {
			colors := int(uint32(entry[46]) | uint32(entry[47])<<8 | uint32(entry[48])<<16 | uint32(entry[49])<<24)
			if colors == 0 {
				colors = 1 << bpp
			}
			palette = colors * 4
		}
		header := make([]byte, 14)
		header[0], header[1] = 'B', 'M'
		total := uint32(14 + 40 + palette + len(entry) - 40)
		header[2] = byte(total)
		header[3] = byte(total >> 8)
		header[4] = byte(total >> 16)
		header[5] = byte(total >> 24)
		pixels := uint32(14 + 40 + palette)
		header[10] = byte(pixels)
		header[11] = byte(pixels >> 8)
		header[12] = byte(pixels >> 16)
		header[13] = byte(pixels >> 24)
		// ICO heights are doubled (XOR + AND masks); halve for the decoder.
		h := int(uint32(entry[8]) | uint32(entry[9])<<8 | uint32(entry[10])<<16 | uint32(entry[11])<<24)
		entry = append([]byte(nil), entry...)
		entry[8] = byte(h / 2)
		entry[9] = byte(h / 2 >> 8)
		entry[10] = byte(h / 2 >> 16)
		entry[11] = byte(h / 2 >> 24)
		return bmp.Decode(bytes.NewReader(append(header, entry...)))
	}
	return nil, errors.New("ico entry format not decodable")
}

// ImageDimensions reads pixel dimensions from common headers.
func ImageDimensions(data []byte) (int, int, bool) {
	if len(data) >= 24 && bytes.Equal(data[0:8], []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}) {
		w := int(uint32(data[16])<<24 | uint32(data[17])<<16 | uint32(data[18])<<8 | uint32(data[19]))
		h := int(uint32(data[20])<<24 | uint32(data[21])<<16 | uint32(data[22])<<8 | uint32(data[23]))
		return w, h, true
	}
	if len(data) >= 10 && data[0] == 'G' && data[1] == 'I' && data[2] == 'F' {
		w := int(data[6]) | int(data[7])<<8
		h := int(data[8]) | int(data[9])<<8
		return w, h, true
	}
	if len(data) >= 26 && data[0] == 'B' && data[1] == 'M' {
		w := int(int32(uint32(data[18]) | uint32(data[19])<<8 | uint32(data[20])<<16 | uint32(data[21])<<24))
		h := int(int32(uint32(data[22]) | uint32(data[23])<<8 | uint32(data[24])<<16 | uint32(data[25])<<24))
		if h < 0 {
			h = -h
		}
		return w, h, true
	}
	if len(data) >= 3 && data[0] == 0xFF && data[1] == 0xD8 {
		// JPEG SOF scan.
		for i := 2; i+9 < len(data); {
			if data[i] != 0xFF {
				i++
				continue
			}
			marker := data[i+1]
			if marker == 0xD8 || (marker >= 0xD0 && marker <= 0xD9) {
				i += 2
				continue
			}
			if i+3 >= len(data) {
				break
			}
			segLen := int(data[i+2])<<8 | int(data[i+3])
			if (marker >= 0xC0 && marker <= 0xCF) && marker != 0xC4 && marker != 0xC8 && marker != 0xCC {
				if i+9 < len(data) {
					h := int(data[i+5])<<8 | int(data[i+6])
					w := int(data[i+7])<<8 | int(data[i+8])
					return w, h, true
				}
			}
			i += 2 + segLen
		}
	}
	if len(data) >= 30 && bytes.Equal(data[0:4], []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WEBP")) {
		if bytes.Equal(data[12:16], []byte("VP8 ")) && len(data) >= 30 {
			w := int(uint16(data[26]) | uint16(data[27])<<8&0x3FFF)
			h := int(uint16(data[28]) | uint16(data[29])<<8&0x3FFF)
			return w & 0x3FFF, h & 0x3FFF, true
		}
		if bytes.Equal(data[12:16], []byte("VP8L")) && len(data) >= 25 {
			bits := uint32(data[21]) | uint32(data[22])<<8 | uint32(data[23])<<16 | uint32(data[24])<<24
			w := int(bits&0x3FFF) + 1
			h := int((bits>>14)&0x3FFF) + 1
			return w, h, true
		}
	}
	return 0, 0, false
}
