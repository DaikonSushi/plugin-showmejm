package main

import (
	"bytes"
	"fmt"
	"image"
	"image/draw"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"os"
	"path/filepath"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/signintech/gopdf"
)

// PDFGenerator handles PDF creation
type PDFGenerator struct {
	config *Config
}

// NewPDFGenerator creates a new PDF generator
func NewPDFGenerator(config *Config) *PDFGenerator {
	return &PDFGenerator{
		config: config,
	}
}

// CreatePDF creates PDF files from downloaded images
func (p *PDFGenerator) CreatePDF(comic *Comic, images []DownloadedImage) ([]string, error) {
	if len(images) == 0 {
		return nil, fmt.Errorf("no images to convert")
	}

	pdfDir := filepath.Join(p.config.BaseDir, comic.ID)
	if err := os.MkdirAll(pdfDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create PDF directory: %w", err)
	}

	pdfFiles := make([]string, 0)

	// Calculate how many PDFs we need
	maxPages := p.config.PDFMaxPages
	if maxPages <= 0 {
		maxPages = len(images)
	}

	// Split images into chunks
	totalChunks := (len(images) + maxPages - 1) / maxPages
	for chunkIdx := 0; chunkIdx < totalChunks; chunkIdx++ {
		start := chunkIdx * maxPages
		end := start + maxPages
		if end > len(images) {
			end = len(images)
		}

		chunk := images[start:end]

		// Generate PDF filename
		var pdfPath string
		if totalChunks == 1 {
			pdfPath = filepath.Join(pdfDir, fmt.Sprintf("%s.pdf", comic.ID))
		} else {
			pdfPath = filepath.Join(pdfDir, fmt.Sprintf("%s-part%d.pdf", comic.ID, chunkIdx+1))
		}

		// Check if PDF already exists and has correct size
		if info, err := os.Stat(pdfPath); err == nil && info.Size() > 1024 {
			pdfFiles = append(pdfFiles, pdfPath)
			continue
		}

		// Create PDF
		if err := p.createSinglePDF(pdfPath, chunk); err != nil {
			return nil, fmt.Errorf("failed to create PDF %s: %w", pdfPath, err)
		}

		// Encrypt PDF if password is configured
		if p.config.PDFPassword != "" {
			if err := p.encryptPDF(pdfPath, p.config.PDFPassword); err != nil {
				return nil, fmt.Errorf("failed to encrypt PDF %s: %w", pdfPath, err)
			}
		}

		pdfFiles = append(pdfFiles, pdfPath)
	}

	return pdfFiles, nil
}

// createSinglePDF creates a single PDF from images
func (p *PDFGenerator) createSinglePDF(pdfPath string, images []DownloadedImage) error {
	pdf := gopdf.GoPdf{}
	pdf.Start(gopdf.Config{PageSize: *gopdf.PageSizeA4})

	// Track temp files for cleanup at the end
	var tempFiles []string
	defer func() {
		for _, tf := range tempFiles {
			os.Remove(tf)
		}
	}()

	// Process images
	for _, img := range images {
		var imagePath string
		var needCompress bool = p.config.ImageQuality > 0 && p.config.ImageQuality < 100

		// Read original image data - we always need this for normalization
		var imgData []byte
		var err error
		if len(img.Data) > 0 {
			imgData = img.Data
		} else {
			imgData, err = os.ReadFile(img.Path)
			if err != nil {
				// Skip this image if can't read
				continue
			}
		}

		if needCompress {
			// Compress image (also normalizes color space)
			compressedData, err := p.compressImage(imgData, p.config.ImageQuality)
			if err != nil {
				// If compression fails, try normalization only
				normalizedData, normErr := p.normalizeImage(imgData)
				if normErr != nil {
					// If normalization also fails, use original path
					imagePath = img.Path
				} else {
					tempFile := img.Path + ".normalized.jpg"
					if writeErr := os.WriteFile(tempFile, normalizedData, 0644); writeErr != nil {
						imagePath = img.Path
					} else {
						imagePath = tempFile
						tempFiles = append(tempFiles, tempFile)
					}
				}
			} else {
				// Write compressed data to temp file
				tempFile := img.Path + ".compressed.jpg"
				if writeErr := os.WriteFile(tempFile, compressedData, 0644); writeErr != nil {
					// If write fails, use original path
					imagePath = img.Path
				} else {
					imagePath = tempFile
					tempFiles = append(tempFiles, tempFile)
				}
			}
		} else {
			// Even without compression, normalize the image to fix color space issues
			normalizedData, err := p.normalizeImage(imgData)
			if err != nil {
				// If normalization fails, use original path
				imagePath = img.Path
			} else {
				tempFile := img.Path + ".normalized.jpg"
				if writeErr := os.WriteFile(tempFile, normalizedData, 0644); writeErr != nil {
					imagePath = img.Path
				} else {
					imagePath = tempFile
					tempFiles = append(tempFiles, tempFile)
				}
			}
		}

		// Read image file to get dimensions
		imgFile, err := os.Open(imagePath)
		if err != nil {
			continue
		}

		imgConfig, _, err := image.DecodeConfig(imgFile)
		imgFile.Close()
		if err != nil {
			// Skip images that can't be decoded
			continue
		}

		// Calculate page dimensions
		pageWidth := float64(imgConfig.Width)
		pageHeight := float64(imgConfig.Height)

		// Scale to reasonable PDF dimensions (max A4 at 150 DPI)
		maxWidth := 1240.0  // A4 width at 150 DPI
		maxHeight := 1754.0 // A4 height at 150 DPI

		scale := 1.0
		if pageWidth > maxWidth {
			scale = maxWidth / pageWidth
		}
		if pageHeight*scale > maxHeight {
			scale = maxHeight / pageHeight
		}

		pageWidth *= scale
		pageHeight *= scale

		// Ensure minimum size
		if pageWidth < 100 {
			pageWidth = 100
		}
		if pageHeight < 100 {
			pageHeight = 100
		}

		// Add page with custom size
		pdf.AddPageWithOption(gopdf.PageOption{
			PageSize: &gopdf.Rect{W: pageWidth, H: pageHeight},
		})

		// Add image to PDF
		err = pdf.Image(imagePath, 0, 0, &gopdf.Rect{W: pageWidth, H: pageHeight})
		if err != nil {
			// Skip this image if it fails
			continue
		}
	}

	// Save PDF
	if err := pdf.WritePdf(pdfPath); err != nil {
		return fmt.Errorf("failed to write PDF: %w", err)
	}

	return nil
}

// compressImage compresses image data to JPEG with specified quality
// It also normalizes the color space to ensure compatibility with PDF generators
func (p *PDFGenerator) compressImage(imgData []byte, quality int) ([]byte, error) {
	// Decode the image
	img, _, err := image.Decode(bytes.NewReader(imgData))
	if err != nil {
		return nil, fmt.Errorf("failed to decode image: %w", err)
	}

	// Convert to RGBA to ensure standard color space
	// This fixes images with missing or non-standard color spaces
	bounds := img.Bounds()
	rgbaImg := image.NewRGBA(bounds)
	draw.Draw(rgbaImg, bounds, img, bounds.Min, draw.Src)

	// Encode to JPEG with specified quality
	var buf bytes.Buffer
	err = jpeg.Encode(&buf, rgbaImg, &jpeg.Options{Quality: quality})
	if err != nil {
		return nil, fmt.Errorf("failed to encode image: %w", err)
	}

	return buf.Bytes(), nil
}

// normalizeImage converts image to standard RGB color space without quality loss
// This is used for images that don't need compression but may have color space issues
func (p *PDFGenerator) normalizeImage(imgData []byte) ([]byte, error) {
	return p.compressImage(imgData, 100) // Use maximum quality for normalization
}

// CreatePDFWithTitle creates a PDF with a title page
func (p *PDFGenerator) CreatePDFWithTitle(comic *Comic, images []DownloadedImage) ([]string, error) {
	// For now, just use the regular CreatePDF
	// Title page can be added in future versions
	return p.CreatePDF(comic, images)
}

// CleanupPDF removes generated PDF files
func (p *PDFGenerator) CleanupPDF(comic *Comic) error {
	pdfDir := filepath.Join(p.config.BaseDir, comic.ID)
	
	entries, err := os.ReadDir(pdfDir)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		if filepath.Ext(entry.Name()) == ".pdf" {
			os.Remove(filepath.Join(pdfDir, entry.Name()))
		}
	}

	return nil
}

// encryptPDF encrypts a PDF file with password using AES-256 encryption
func (p *PDFGenerator) encryptPDF(pdfPath string, password string) error {
	// Create encryption configuration with AES-256
	// User password: required to open the PDF
	// Owner password: same as user password for simplicity
	conf := model.NewAESConfiguration(password, password, 256)

	// Relax validation to avoid color space validation errors
	// This is needed because some images may have non-standard color spaces
	conf.ValidationMode = model.ValidationRelaxed

	// Create a temporary output file path
	encryptedPath := pdfPath + ".encrypted"

	// Encrypt the PDF file
	if err := api.EncryptFile(pdfPath, encryptedPath, conf); err != nil {
		return fmt.Errorf("encryption failed: %w", err)
	}

	// Replace original file with encrypted version
	if err := os.Remove(pdfPath); err != nil {
		os.Remove(encryptedPath) // Clean up on failure
		return fmt.Errorf("failed to remove original file: %w", err)
	}

	if err := os.Rename(encryptedPath, pdfPath); err != nil {
		return fmt.Errorf("failed to rename encrypted file: %w", err)
	}

	return nil
}
