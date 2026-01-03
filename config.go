package main

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Config holds plugin configuration
type Config struct {
	// Download settings
	BaseDir     string `json:"base_dir"`      // Download directory
	BatchSize   int    `json:"batch_size"`    // Images per batch for download
	PDFMaxPages int    `json:"pdf_max_pages"` // Max pages per PDF file

	// Image compression settings
	ImageQuality int `json:"image_quality"` // JPEG compression quality (1-100, 0 means no compression)

	// Feature flags
	AutoFindJM     bool   `json:"auto_find_jm"`     // Auto-find JM numbers in messages
	PreventDefault bool   `json:"prevent_default"`  // Stop other plugins from handling
	PDFPassword    string `json:"pdf_password"`     // PDF encryption password (for display only)
	CleanupAfter   bool   `json:"cleanup_after"`    // Delete images after PDF creation

	// Whitelist (empty means allow all)
	PersonWhitelist []int64 `json:"person_whitelist"` // Person whitelist
	GroupWhitelist  []int64 `json:"group_whitelist"`  // Group whitelist

	// JM API settings
	JMDomains          []string `json:"jm_domains"`          // Available JM domains
	ConcurrentDownload int      `json:"concurrent_download"` // Max concurrent image downloads
}

// DefaultConfig returns default configuration
func DefaultConfig() *Config {
	return &Config{
		BaseDir:            "/shared-data/jmDownload", // Shared directory with napcat container
		BatchSize:          20,
		PDFMaxPages:        200,
		ImageQuality:       0, // 0 means no compression, 1-100 for JPEG quality
		AutoFindJM:         true,
		PreventDefault:     true,
		PDFPassword:        "",
		CleanupAfter:       false,
		PersonWhitelist:    []int64{},
		GroupWhitelist:     []int64{},
		JMDomains:          []string{},
		ConcurrentDownload: 10,
	}
}

// LoadConfig loads configuration from file
func LoadConfig() (*Config, error) {
	configPath := filepath.Join("plugins-config", "showmejm", "config.json")

	// Create config directory if not exists
	configDir := filepath.Dir(configPath)
	if err := os.MkdirAll(configDir, 0755); err != nil {
		return nil, err
	}

	// If config doesn't exist, create default
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		config := DefaultConfig()
		if err := config.Save(configPath); err != nil {
			return nil, err
		}
		return config, nil
	}

	// Load existing config
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, err
	}

	config := DefaultConfig()
	if err := json.Unmarshal(data, config); err != nil {
		return nil, err
	}

	// Ensure default values for new fields
	if config.ConcurrentDownload <= 0 {
		config.ConcurrentDownload = 10
	}

	// Validate image quality range
	if config.ImageQuality < 0 {
		config.ImageQuality = 0
	} else if config.ImageQuality > 100 {
		config.ImageQuality = 100
	}

	// Create base directory if not exists
	if err := os.MkdirAll(config.BaseDir, 0755); err != nil {
		return nil, err
	}

	return config, nil
}

// Save saves configuration to file
func (c *Config) Save(path string) error {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

// CheckWhitelist checks if user/group is in whitelist
func (c *Config) CheckWhitelist(isGroup bool, id int64) bool {
	var whitelist []int64
	if isGroup {
		whitelist = c.GroupWhitelist
	} else {
		whitelist = c.PersonWhitelist
	}

	// Empty whitelist means allow all
	if len(whitelist) == 0 {
		return true
	}

	for _, wid := range whitelist {
		if wid == id {
			return true
		}
	}
	return false
}

// AddToWhitelist adds an ID to the whitelist
func (c *Config) AddToWhitelist(isGroup bool, id int64) {
	if isGroup {
		// Check if already exists
		for _, wid := range c.GroupWhitelist {
			if wid == id {
				return
			}
		}
		c.GroupWhitelist = append(c.GroupWhitelist, id)
	} else {
		// Check if already exists
		for _, wid := range c.PersonWhitelist {
			if wid == id {
				return
			}
		}
		c.PersonWhitelist = append(c.PersonWhitelist, id)
	}
}

// RemoveFromWhitelist removes an ID from the whitelist
func (c *Config) RemoveFromWhitelist(isGroup bool, id int64) {
	if isGroup {
		newList := make([]int64, 0, len(c.GroupWhitelist))
		for _, wid := range c.GroupWhitelist {
			if wid != id {
				newList = append(newList, wid)
			}
		}
		c.GroupWhitelist = newList
	} else {
		newList := make([]int64, 0, len(c.PersonWhitelist))
		for _, wid := range c.PersonWhitelist {
			if wid != id {
				newList = append(newList, wid)
			}
		}
		c.PersonWhitelist = newList
	}
}
