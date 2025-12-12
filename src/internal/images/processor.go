package images

import (
	"fmt"
	"strings"

	"github.com/h2non/bimg"
)

// ImageSize definuje rozměry pro různé varianty obrázků
type ImageSize struct {
	Width  int
	Height int
}

var (
	SizeThumb = ImageSize{Width: 150, Height: 150}
	SizeSm    = ImageSize{Width: 400, Height: 400}
	SizeMd    = ImageSize{Width: 800, Height: 800}
	SizeLg    = ImageSize{Width: 1200, Height: 1200}
)

// ResizeImage změní velikost obrázku při zachování aspect ratio pomocí libvips
// Obrázek se vejde do zadaného rozměru (fit inside)
func ResizeImage(data []byte, mimeType string, size ImageSize) ([]byte, error) {
	// Vytvoření bimg image
	image := bimg.NewImage(data)

	// Získání metadat (rozměry, formát)
	metadata, err := image.Metadata()
	if err != nil {
		return nil, fmt.Errorf("failed to read image metadata: %w", err)
	}

	// Kontrola, zda je potřeba resize (nesnažíme se zvětšovat)
	if metadata.Size.Width <= size.Width && metadata.Size.Height <= size.Height {
		return data, nil
	}

	// Volba kvality podle výstupní velikosti
	quality := 90
	if size.Width <= 150 { // thumb
		quality = 85
	} else if size.Width <= 400 { // sm
		quality = 88
	}

	// Konfigurace resize operace
	options := bimg.Options{
		Width:   size.Width,
		Height:  size.Height,
		Quality: quality,
		Crop:    false,   // false = fit (aspect ratio preserved), true = fill
		Embed:   false,   // false = shrink only
		Rotate:  bimg.D0, // Auto-rotation je řešena automaticky v libvips
	}

	// Určení výstupního formátu
	isPNG := strings.Contains(mimeType, "png")

	// PNG thumbnaily ponecháme jako PNG pro lepší kvalitu
	if isPNG && size.Width <= 150 {
		options.Type = bimg.PNG
	} else if isPNG && size.Width > 400 {
		// Větší PNG ponecháme jako PNG
		options.Type = bimg.PNG
	} else {
		// JPEG pro ostatní (rychlejší a menší)
		options.Type = bimg.JPEG
	}

	// Provedení resize
	resized, err := image.Process(options)
	if err != nil {
		return nil, fmt.Errorf("failed to resize image: %w", err)
	}

	return resized, nil
}

// calculateAspectRatioFit vypočítá nové rozměry při zachování aspect ratio
// Obrázek se vejde do maxWidth x maxHeight
func calculateAspectRatioFit(srcWidth, srcHeight, maxWidth, maxHeight int) (int, int) {
	// Pokud se obrázek vejde, vrátíme původní rozměry
	if srcWidth <= maxWidth && srcHeight <= maxHeight {
		return srcWidth, srcHeight
	}

	// Výpočet scale faktorů
	widthRatio := float64(maxWidth) / float64(srcWidth)
	heightRatio := float64(maxHeight) / float64(srcHeight)

	// Použijeme menší ratio (aby se obrázek vešel)
	ratio := widthRatio
	if heightRatio < widthRatio {
		ratio = heightRatio
	}

	// Výpočet nových rozměrů
	newWidth := int(float64(srcWidth) * ratio)
	newHeight := int(float64(srcHeight) * ratio)

	return newWidth, newHeight
}

// IsImageMimeType zjistí, zda je MIME typ obrázek
func IsImageMimeType(mimeType string) bool {
	return strings.HasPrefix(mimeType, "image/")
}

// IsPDFMimeType zjistí, zda je MIME typ PDF
func IsPDFMimeType(mimeType string) bool {
	return mimeType == "application/pdf"
}

// GetOutputMimeType vrátí MIME typ pro výstupní obrázek
func GetOutputMimeType(inputMimeType string) string {
	if strings.Contains(inputMimeType, "png") {
		return "image/png"
	}
	// Pro ostatní včetně JPEG a PDF náhledů
	return "image/jpeg"
}
