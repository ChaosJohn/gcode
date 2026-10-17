// Package pdf ports jcode-pdf: PDF text extraction with panic recovery.
// The Go port uses github.com/ledongthuc/pdf (pure Go, no CGO); extraction
// errors surface as normal errors, never as panics.
package pdf

import (
	"fmt"
	"os"
	"strings"

	ledong "github.com/ledongthuc/pdf"
)

// ExtractText extracts the text of a PDF file, pages separated by form feed
// (matching pdf_extract's \x0c page separator).
func ExtractText(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	reader, err := ledong.NewReader(file, info.Size())
	if err != nil {
		return "", err
	}
	var pages []string
	total := reader.NumPage()
	for i := 1; i <= total; i++ {
		func() {
			defer func() {
				if recover() != nil {
					// A single bad page must not kill the whole extraction.
					pages = append(pages, "")
				}
			}()
			page := reader.Page(i)
			text, err := page.GetPlainText(nil)
			if err != nil {
				pages = append(pages, "")
				return
			}
			pages = append(pages, text)
		}()
	}
	return strings.Join(pages, "\x0c"), nil
}

var _ = fmt.Sprintf
