package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// Downloader handles comic image downloads
type Downloader struct {
	client *JMClient
	config *Config
}

// DownloadedImage represents a downloaded image
type DownloadedImage struct {
	Index    int
	Path     string
	Data     []byte
	Filename string
}

// NewDownloader creates a new downloader
func NewDownloader(client *JMClient, config *Config) *Downloader {
	return &Downloader{
		client: client,
		config: config,
	}
}

// DownloadComic downloads all images for a comic
func (d *Downloader) DownloadComic(comic *Comic) ([]DownloadedImage, error) {
	// Create download directory
	downloadDir := filepath.Join(d.config.BaseDir, comic.ID)
	if err := os.MkdirAll(downloadDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create download directory: %w", err)
	}

	// Check if already downloaded
	existingImages, err := d.checkExistingImages(downloadDir, comic)
	if err == nil && len(existingImages) > 0 && len(existingImages) >= comic.Pages {
		return existingImages, nil
	}

	// Collect all images from all chapters
	allImages := make([]DownloadedImage, 0, comic.Pages)
	imageIndex := 0

	for _, chapter := range comic.Chapters {
		images, err := d.downloadChapter(&chapter, downloadDir, imageIndex)
		if err != nil {
			return nil, fmt.Errorf("failed to download chapter %s: %w", chapter.ID, err)
		}
		allImages = append(allImages, images...)
		imageIndex += len(images)
	}

	// Sort images by index
	sort.Slice(allImages, func(i, j int) bool {
		return allImages[i].Index < allImages[j].Index
	})

	return allImages, nil
}

// downloadChapter downloads all images for a chapter
func (d *Downloader) downloadChapter(chapter *Chapter, downloadDir string, startIndex int) ([]DownloadedImage, error) {
	images := make([]DownloadedImage, len(chapter.ImageURLs))
	var wg sync.WaitGroup
	var mu sync.Mutex
	errors := make([]error, 0)

	// Limit concurrent downloads
	semaphore := make(chan struct{}, 10)

	for i, imageURL := range chapter.ImageURLs {
		wg.Add(1)
		go func(index int, url string) {
			defer wg.Done()
			semaphore <- struct{}{}
			defer func() { <-semaphore }()

			// Get filename from URL or from ImageNames
			filename := ""
			if index < len(chapter.ImageNames) {
				filename = chapter.ImageNames[index]
			} else {
				// Extract from URL
				parts := strings.Split(url, "/")
				if len(parts) > 0 {
					filename = parts[len(parts)-1]
					// Remove query parameters
					if idx := strings.Index(filename, "?"); idx > 0 {
						filename = filename[:idx]
					}
				}
			}

			// Download image
			data, err := d.client.DownloadImage(url)
			if err != nil {
				mu.Lock()
				errors = append(errors, fmt.Errorf("failed to download image %d (%s): %w", index, url, err))
				mu.Unlock()
				return
			}

			// Decode scrambled image
			decodedData, err := d.client.DecodeScrambledImage(data, chapter, filename)
			if err != nil {
				// Use original data if decoding fails
				decodedData = data
			}

			// Determine file extension
			ext := filepath.Ext(filename)
			if ext == "" {
				ext = ".jpg"
			}

			// Save to file
			globalIndex := startIndex + index
			imagePath := filepath.Join(downloadDir, fmt.Sprintf("%04d%s", globalIndex, ext))
			if err := os.WriteFile(imagePath, decodedData, 0644); err != nil {
				mu.Lock()
				errors = append(errors, fmt.Errorf("failed to save image %d: %w", index, err))
				mu.Unlock()
				return
			}

			mu.Lock()
			images[index] = DownloadedImage{
				Index:    globalIndex,
				Path:     imagePath,
				Data:     decodedData,
				Filename: filename,
			}
			mu.Unlock()
		}(i, imageURL)
	}

	wg.Wait()

	if len(errors) > 0 {
		return nil, fmt.Errorf("download errors: %v", errors[0])
	}

	// Filter out empty entries
	result := make([]DownloadedImage, 0, len(images))
	for _, img := range images {
		if img.Path != "" {
			result = append(result, img)
		}
	}

	return result, nil
}

// checkExistingImages checks if images are already downloaded
func (d *Downloader) checkExistingImages(dir string, comic *Comic) ([]DownloadedImage, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	images := make([]DownloadedImage, 0)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		name := entry.Name()
		ext := strings.ToLower(filepath.Ext(name))
		
		// Only include image files
		if ext != ".jpg" && ext != ".jpeg" && ext != ".png" && ext != ".webp" && ext != ".gif" {
			continue
		}

		imagePath := filepath.Join(dir, name)
		data, err := os.ReadFile(imagePath)
		if err != nil {
			continue
		}

		// Extract index from filename
		indexStr := strings.TrimSuffix(name, ext)
		index := 0
		fmt.Sscanf(indexStr, "%d", &index)

		images = append(images, DownloadedImage{
			Index:    index,
			Path:     imagePath,
			Data:     data,
			Filename: name,
		})
	}

	// Sort by index
	sort.Slice(images, func(i, j int) bool {
		return images[i].Index < images[j].Index
	})

	return images, nil
}

// CleanupDownload removes downloaded files
func (d *Downloader) CleanupDownload(comic *Comic) error {
	downloadDir := filepath.Join(d.config.BaseDir, comic.ID)
	return os.RemoveAll(downloadDir)
}
