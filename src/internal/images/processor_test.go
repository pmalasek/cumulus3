package images

import (
	"testing"
)

func TestCalculateAspectRatioFit(t *testing.T) {
	tests := []struct {
		name      string
		srcW      int
		srcH      int
		maxW      int
		maxH      int
		expectedW int
		expectedH int
	}{
		{
			name:      "landscape image fits in bounds",
			srcW:      1920,
			srcH:      1080,
			maxW:      800,
			maxH:      800,
			expectedW: 800,
			expectedH: 450,
		},
		{
			name:      "portrait image fits in bounds",
			srcW:      1080,
			srcH:      1920,
			maxW:      800,
			maxH:      800,
			expectedW: 450,
			expectedH: 800,
		},
		{
			name:      "small image remains unchanged",
			srcW:      200,
			srcH:      200,
			maxW:      800,
			maxH:      800,
			expectedW: 200,
			expectedH: 200,
		},
		{
			name:      "square image",
			srcW:      1000,
			srcH:      1000,
			maxW:      400,
			maxH:      400,
			expectedW: 400,
			expectedH: 400,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w, h := calculateAspectRatioFit(tt.srcW, tt.srcH, tt.maxW, tt.maxH)
			if w != tt.expectedW || h != tt.expectedH {
				t.Errorf("calculateAspectRatioFit(%d, %d, %d, %d) = (%d, %d), want (%d, %d)",
					tt.srcW, tt.srcH, tt.maxW, tt.maxH, w, h, tt.expectedW, tt.expectedH)
			}
		})
	}
}

func TestIsImageMimeType(t *testing.T) {
	tests := []struct {
		mimeType string
		expected bool
	}{
		{"image/jpeg", true},
		{"image/png", true},
		{"image/gif", true},
		{"application/pdf", false},
		{"text/plain", false},
		{"video/mp4", false},
	}

	for _, tt := range tests {
		t.Run(tt.mimeType, func(t *testing.T) {
			result := IsImageMimeType(tt.mimeType)
			if result != tt.expected {
				t.Errorf("IsImageMimeType(%s) = %v, want %v", tt.mimeType, result, tt.expected)
			}
		})
	}
}

func TestIsPDFMimeType(t *testing.T) {
	tests := []struct {
		mimeType string
		expected bool
	}{
		{"application/pdf", true},
		{"image/jpeg", false},
		{"text/plain", false},
	}

	for _, tt := range tests {
		t.Run(tt.mimeType, func(t *testing.T) {
			result := IsPDFMimeType(tt.mimeType)
			if result != tt.expected {
				t.Errorf("IsPDFMimeType(%s) = %v, want %v", tt.mimeType, result, tt.expected)
			}
		})
	}
}
