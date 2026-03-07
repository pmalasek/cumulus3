package images

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/h2non/bimg"
)

// GeneratePDFThumbnail vygeneruje náhled první stránky PDF jako JPEG.
// pdftoppm vyrenderuje stránku jako PNG, bimg ji přeškáluje stejnou cestou jako obrázky.
func GeneratePDFThumbnail(pdfData []byte, size ImageSize) ([]byte, error) {
	tmpDir, err := os.MkdirTemp("", "pdf-thumb-*")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	pdfPath := filepath.Join(tmpDir, "input.pdf")
	if err := os.WriteFile(pdfPath, pdfData, 0644); err != nil {
		return nil, fmt.Errorf("failed to write temp PDF: %w", err)
	}

	maxDim := size.Width
	if size.Height > maxDim {
		maxDim = size.Height
	}

	// pdftoppm renders the first page to <tmpDir>/output.png
	cmd := exec.Command("pdftoppm",
		"-png",
		"-f", "1",
		"-l", "1",
		"-singlefile",
		"-scale-to", fmt.Sprintf("%d", maxDim*2), // 2× for better quality before downscale
		pdfPath,
		filepath.Join(tmpDir, "output"),
	)

	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("pdftoppm failed: %w, stderr: %s", err, stderr.String())
	}

	imgData, err := os.ReadFile(filepath.Join(tmpDir, "output.png"))
	if err != nil {
		return nil, fmt.Errorf("failed to read generated PNG: %w", err)
	}

	// Delegate resize + JPEG encoding to bimg (same pipeline as processor.go).
	// PDF thumbnails are always JPEG for compactness.
	return resizeToPNG(imgData, size)
}

// resizeToPNG applies aspect-ratio-preserving resize via bimg and returns JPEG bytes.
func resizeToPNG(imgData []byte, size ImageSize) ([]byte, error) {
	img := bimg.NewImage(imgData)

	metadata, err := img.Metadata()
	if err != nil {
		return nil, fmt.Errorf("failed to read image metadata: %w", err)
	}

	newWidth, newHeight := calculateAspectRatioFit(
		metadata.Size.Width, metadata.Size.Height,
		size.Width, size.Height,
	)

	options := bimg.Options{
		Width:   newWidth,
		Height:  newHeight,
		Quality: 85,
		Type:    bimg.JPEG,
		Force:   true,
		Enlarge: false,
	}

	result, err := img.Process(options)
	if err != nil {
		return nil, fmt.Errorf("failed to resize PDF thumbnail: %w", err)
	}

	return result, nil
}
