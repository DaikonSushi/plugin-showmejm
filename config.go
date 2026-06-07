package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// Config holds plugin configuration
type Config struct {
	// Download settings
	BaseDir          string `json:"base_dir"`             // Download directory
	BatchSize        int    `json:"batch_size"`           // Images per batch for download
	PDFMaxPages      int    `json:"pdf_max_pages"`        // Max pages per PDF file
	PDFMaxFileSizeMB int    `json:"pdf_max_file_size_mb"` // Max size per PDF file (MB); generated PDF will be split into multiple parts if it exceeds this limit. 0 means no limit.

	// Image compression settings
	ImageQuality    int `json:"image_quality"`      // JPEG compression quality (1-100, 0 means no compression)
	MaxPDFFileCount int `json:"max_pdf_file_count"` // Max number of PDF files to send; compression is increased until this is met

	// Feature flags
	AutoFindJM              bool   `json:"auto_find_jm"`               // Auto-find JM numbers in messages
	PreventDefault          bool   `json:"prevent_default"`            // Stop other plugins from handling
	NotifyStatusInGroup     bool   `json:"notify_status_in_group"`     // Send text status/debug messages in groups; false sends them to admins only
	PDFPassword             string `json:"pdf_password"`               // PDF encryption password (for display only)
	CleanupAfter            bool   `json:"cleanup_after"`              // Delete images after PDF creation
	MaxPagesWithoutAdmin    int    `json:"max_pages_without_admin"`    // Non-admin page limit; 0 means no limit
	UploadRetryCount        int    `json:"upload_retry_count"`         // Upload retry count per PDF
	UploadRetryDelaySeconds int    `json:"upload_retry_delay_seconds"` // Delay between upload retries

	// Whitelist (empty means allow all)
	PersonWhitelist []int64 `json:"person_whitelist"` // Person whitelist
	GroupWhitelist  []int64 `json:"group_whitelist"`  // Group whitelist
	AdminUsers      []int64 `json:"admin_users"`      // Admin QQ IDs used for plugin-side fallback and contact hints

	// JM API settings
	JMDomains       []string `json:"jm_domains"`         // Available JM web domains
	JMAPIEnabled    bool     `json:"jm_api_enabled"`     // Enable mobile API fallback/client
	JMAPIFirst      bool     `json:"jm_api_first"`       // Try mobile API before web HTML
	JMAPIDomains    []string `json:"jm_api_domains"`     // Mobile API domains; empty uses built-in defaults
	JMAPIAppVersion string   `json:"jm_api_app_version"` // Mobile API app version

	ConcurrentDownload int `json:"concurrent_download"` // Max concurrent image downloads per task

	// Task-level concurrency
	MaxConcurrentTasks int `json:"max_concurrent_tasks"` // Max concurrent comic-download tasks across the whole plugin
}

// DefaultConfig returns default configuration
func DefaultConfig() *Config {
	return &Config{
		BaseDir:                 "/shared-data/jmDownload", // Shared directory with napcat container
		BatchSize:               20,
		PDFMaxPages:             0,
		PDFMaxFileSizeMB:        180,
		ImageQuality:            65,
		MaxPDFFileCount:         3,
		AutoFindJM:              false,
		PreventDefault:          true,
		NotifyStatusInGroup:     false,
		PDFPassword:             "",
		CleanupAfter:            false,
		MaxPagesWithoutAdmin:    400,
		UploadRetryCount:        3,
		UploadRetryDelaySeconds: 5,
		PersonWhitelist:         []int64{},
		GroupWhitelist:          []int64{},
		AdminUsers:              []int64{2577954317},
		JMDomains:               []string{},
		JMAPIEnabled:            true,
		JMAPIFirst:              false,
		JMAPIDomains:            []string{},
		JMAPIAppVersion:         APP_VERSION,
		ConcurrentDownload:      10,
		MaxConcurrentTasks:      2,
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
	if config.MaxConcurrentTasks <= 0 {
		config.MaxConcurrentTasks = 2
	}
	if config.PDFMaxFileSizeMB < 0 {
		config.PDFMaxFileSizeMB = 0
	}
	// Migrate old defaults so existing deployments prefer fewer PDF files.
	if config.PDFMaxPages == 200 {
		config.PDFMaxPages = 0
	}
	if config.PDFMaxFileSizeMB == 45 {
		config.PDFMaxFileSizeMB = 180
	}
	if config.MaxPDFFileCount <= 0 {
		config.MaxPDFFileCount = 3
	}
	if config.MaxPagesWithoutAdmin <= 0 {
		config.MaxPagesWithoutAdmin = 400
	}
	if config.UploadRetryCount <= 0 {
		config.UploadRetryCount = 3
	}
	if config.UploadRetryDelaySeconds <= 0 {
		config.UploadRetryDelaySeconds = 5
	}
	if config.JMAPIAppVersion == "" {
		config.JMAPIAppVersion = APP_VERSION
	}

	// Validate image quality range
	if config.ImageQuality <= 0 {
		config.ImageQuality = 65
	} else if config.ImageQuality > 100 {
		config.ImageQuality = 100
	}

	// Create base directory if not exists
	if err := os.MkdirAll(config.BaseDir, 0755); err != nil {
		return nil, err
	}

	return config, nil
}

// UploadRetryDelay returns the configured upload retry delay.
func (c *Config) UploadRetryDelay() time.Duration {
	return time.Duration(c.UploadRetryDelaySeconds) * time.Second
}

// IsPluginAdmin returns true if userID is configured as a plugin admin.
func (c *Config) IsPluginAdmin(userID int64) bool {
	for _, admin := range c.AdminUsers {
		if admin == userID {
			return true
		}
	}
	return false
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
