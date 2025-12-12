package images

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"strings"

	_ "image/gif"

	"github.com/rwcarlsen/goexif/exif"
	"golang.org/x/image/draw"
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

// ResizeImage změní velikost obrázku při zachování aspect ratio
// Obrázek se vejde do zadaného rozměru (fit inside)
func ResizeImage(data []byte, mimeType string, size ImageSize) ([]byte, error) {
	// Dekódování obrázku
	img, format, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("failed to decode image: %w", err)
	}

	// Aplikace EXIF orientace (pokud existuje) - jen pro JPEG
	if format == "jpeg" || format == "jpg" {
		img = applyOrientation(img, data)
	}

	// Získání původních rozměrů (po aplikaci orientace)
	bounds := img.Bounds()
	origWidth := bounds.Dx()
	origHeight := bounds.Dy()

	// Výpočet nových rozměrů při zachování aspect ratio
	newWidth, newHeight := calculateAspectRatioFit(origWidth, origHeight, size.Width, size.Height)

	// Pokud jsou rozměry stejné nebo větší (není potřeba zvětšovat), vrátíme originál
	if newWidth >= origWidth && newHeight >= origHeight {
		return data, nil
	}

	// Vytvoření nového obrázku s vypočtenými rozměry
	dst := image.NewRGBA(image.Rect(0, 0, newWidth, newHeight))

	// Výběr resample algoritmu podle velikosti výstupu
	// Pro thumbnaily použijeme rychlejší ApproxBiLinear (2-3x rychlejší)
	// Pro větší velikosti použijeme CatmullRom (vyšší kvalita)
	if size.Width <= 400 { // thumb a sm
		draw.ApproxBiLinear.Scale(dst, dst.Bounds(), img, bounds, draw.Over, nil)
	} else {
		draw.CatmullRom.Scale(dst, dst.Bounds(), img, bounds, draw.Over, nil)
	}

	// Enkódování výsledku
	var buf bytes.Buffer

	// Volba kvality podle výstupní velikosti
	quality := 90
	if size.Width <= 150 { // thumb
		quality = 80
	} else if size.Width <= 400 { // sm
		quality = 85
	}

	switch format {
	case "jpeg", "jpg":
		err = jpeg.Encode(&buf, dst, &jpeg.Options{Quality: quality})
	case "png":
		// PNG může být pomalé pro velké obrázky, pro menší velikosti použijeme JPEG
		if size.Width <= 400 {
			err = jpeg.Encode(&buf, dst, &jpeg.Options{Quality: quality})
		} else {
			err = png.Encode(&buf, dst)
		}
	case "gif":
		// Pro GIF použijeme JPEG (rychlejší než PNG)
		err = jpeg.Encode(&buf, dst, &jpeg.Options{Quality: quality})
	default:
		// Pro ostatní formáty použijeme JPEG
		err = jpeg.Encode(&buf, dst, &jpeg.Options{Quality: quality})
	}

	if err != nil {
		return nil, fmt.Errorf("failed to encode image: %w", err)
	}

	return buf.Bytes(), nil
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

// applyOrientation aplikuje EXIF orientaci na obrázek
func applyOrientation(img image.Image, data []byte) image.Image {
	// Pokusíme se načíst EXIF data
	x, err := exif.Decode(bytes.NewReader(data))
	if err != nil {
		// Žádná EXIF data nebo chyba - vrátíme originál
		return img
	}

	// Získáme orientaci
	orientation, err := x.Get(exif.Orientation)
	if err != nil {
		// Žádná orientace - vrátíme originál
		return img
	}

	orientVal, err := orientation.Int(0)
	if err != nil {
		return img
	}

	// Aplikujeme transformaci podle EXIF orientace
	// http://sylvana.net/jpegcrop/exif_orientation.html
	switch orientVal {
	case 1:
		// Normal - žádná transformace
		return img
	case 2:
		// Flip horizontal
		return flipHorizontal(img)
	case 3:
		// Rotate 180
		return rotate180(img)
	case 4:
		// Flip vertical
		return flipVertical(img)
	case 5:
		// Rotate 90 CW and flip horizontal
		return flipHorizontal(rotate90(img))
	case 6:
		// Rotate 90 CW
		return rotate90(img)
	case 7:
		// Rotate 270 CW and flip horizontal
		return flipHorizontal(rotate270(img))
	case 8:
		// Rotate 270 CW
		return rotate270(img)
	default:
		return img
	}
}

// rotate90 otočí obrázek o 90° ve směru hodinových ručiček
func rotate90(img image.Image) image.Image {
	bounds := img.Bounds()
	width := bounds.Dx()
	height := bounds.Dy()

	// Pro RGBA obrázky použijeme rychlejší přístup
	if rgba, ok := img.(*image.RGBA); ok {
		rotated := image.NewRGBA(image.Rect(0, 0, height, width))
		for y := 0; y < height; y++ {
			for x := 0; x < width; x++ {
				rotated.SetRGBA(height-1-y, x, rgba.RGBAAt(x, y))
			}
		}
		return rotated
	}

	// Fallback pro ostatní typy
	rotated := image.NewRGBA(image.Rect(0, 0, height, width))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			rotated.Set(height-1-y, x, img.At(x, y))
		}
	}

	return rotated
}

// rotate180 otočí obrázek o 180°
func rotate180(img image.Image) image.Image {
	bounds := img.Bounds()
	width := bounds.Dx()
	height := bounds.Dy()

	// Pro RGBA obrázky použijeme rychlejší přístup
	if rgba, ok := img.(*image.RGBA); ok {
		rotated := image.NewRGBA(image.Rect(0, 0, width, height))
		for y := 0; y < height; y++ {
			for x := 0; x < width; x++ {
				rotated.SetRGBA(width-1-x, height-1-y, rgba.RGBAAt(x, y))
			}
		}
		return rotated
	}

	// Fallback pro ostatní typy
	rotated := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			rotated.Set(width-1-x, height-1-y, img.At(x, y))
		}
	}

	return rotated
}

// rotate270 otočí obrázek o 270° ve směru hodinových ručiček (nebo 90° proti)
func rotate270(img image.Image) image.Image {
	bounds := img.Bounds()
	width := bounds.Dx()
	height := bounds.Dy()

	// Pro RGBA obrázky použijeme rychlejší přístup
	if rgba, ok := img.(*image.RGBA); ok {
		rotated := image.NewRGBA(image.Rect(0, 0, height, width))
		for y := 0; y < height; y++ {
			for x := 0; x < width; x++ {
				rotated.SetRGBA(y, width-1-x, rgba.RGBAAt(x, y))
			}
		}
		return rotated
	}

	// Fallback pro ostatní typy
	rotated := image.NewRGBA(image.Rect(0, 0, height, width))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			rotated.Set(y, width-1-x, img.At(x, y))
		}
	}

	return rotated
}

// flipHorizontal převrátí obrázek horizontálně
func flipHorizontal(img image.Image) image.Image {
	bounds := img.Bounds()
	width := bounds.Dx()
	height := bounds.Dy()

	// Pro RGBA obrázky použijeme rychlejší přístup
	if rgba, ok := img.(*image.RGBA); ok {
		flipped := image.NewRGBA(image.Rect(0, 0, width, height))
		for y := 0; y < height; y++ {
			for x := 0; x < width; x++ {
				flipped.SetRGBA(width-1-x, y, rgba.RGBAAt(x, y))
			}
		}
		return flipped
	}

	// Fallback pro ostatní typy
	flipped := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			flipped.Set(width-1-x, y, img.At(x, y))
		}
	}

	return flipped
}

// flipVertical převrátí obrázek vertikálně
func flipVertical(img image.Image) image.Image {
	bounds := img.Bounds()
	width := bounds.Dx()
	height := bounds.Dy()

	// Pro RGBA obrázky použijeme rychlejší přístup
	if rgba, ok := img.(*image.RGBA); ok {
		flipped := image.NewRGBA(image.Rect(0, 0, width, height))
		for y := 0; y < height; y++ {
			for x := 0; x < width; x++ {
				flipped.SetRGBA(x, height-1-y, rgba.RGBAAt(x, y))
			}
		}
		return flipped
	}

	// Fallback pro ostatní typy
	flipped := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			flipped.Set(x, height-1-y, img.At(x, y))
		}
	}

	return flipped
}
