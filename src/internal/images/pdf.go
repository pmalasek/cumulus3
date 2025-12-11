package images

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"

	"golang.org/x/image/draw"
)

// GeneratePDFThumbnail vygeneruje náhled první stránky PDF jako PNG
func GeneratePDFThumbnail(pdfData []byte, size ImageSize) ([]byte, error) {
	// Vytvoříme dočasný adresář pro PDF
	tmpDir, err := os.MkdirTemp("", "pdf-thumb-*")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	// Uložíme PDF do dočasného souboru
	pdfPath := filepath.Join(tmpDir, "input.pdf")
	if err := os.WriteFile(pdfPath, pdfData, 0644); err != nil {
		return nil, fmt.Errorf("failed to write temp PDF: %w", err)
	}

	// Cesta k výstupnímu PNG souboru
	outputPath := filepath.Join(tmpDir, "output.png")

	// Konverze pomocí pdftoppm (součást poppler-utils)
	// -png = výstup jako PNG
	// -f 1 -l 1 = pouze první stránka
	// -singlefile = jeden soubor bez přípony čísla stránky
	// -scale-to = maximální rozměr (zachová aspect ratio)
	maxDim := size.Width
	if size.Height > maxDim {
		maxDim = size.Height
	}

	cmd := exec.Command("pdftoppm",
		"-png",
		"-f", "1",
		"-l", "1",
		"-singlefile",
		"-scale-to", fmt.Sprintf("%d", maxDim*2), // 2x pro lepší kvalitu
		pdfPath,
		filepath.Join(tmpDir, "output"),
	)

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("pdftoppm failed: %w, stderr: %s", err, stderr.String())
	}

	// Načteme vygenerovaný PNG
	imgData, err := os.ReadFile(outputPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read generated PNG: %w", err)
	}

	// Načteme obrázek pro případný resize
	img, err := png.Decode(bytes.NewReader(imgData))
	if err != nil {
		return nil, fmt.Errorf("failed to decode generated PNG: %w", err)
	}

	// Získání rozměrů
	bounds := img.Bounds()
	origWidth := bounds.Dx()
	origHeight := bounds.Dy()

	// Výpočet finálních rozměrů
	newWidth, newHeight := calculateAspectRatioFit(origWidth, origHeight, size.Width, size.Height)

	// Pokud je potřeba resize
	if newWidth != origWidth || newHeight != origHeight {
		dst := image.NewRGBA(image.Rect(0, 0, newWidth, newHeight))
		draw.CatmullRom.Scale(dst, dst.Bounds(), img, bounds, draw.Over, nil)
		img = dst
	}

	// Enkódování jako JPEG (lepší komprese pro náhledy)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 85}); err != nil {
		return nil, fmt.Errorf("failed to encode thumbnail: %w", err)
	}

	return buf.Bytes(), nil
}
