package main

import (
	"bytes"
	"crypto/md5"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/draw"
	"image/jpeg"
	_ "image/png"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// JMClient handles JM API requests
type JMClient struct {
	config       *Config
	httpClient   *http.Client
	domains      []string
	baseURL      string
	imgDomain    string
	mu           sync.RWMutex
	maxPageCache map[string]*maxPageCacheEntry
}

// maxPageCacheEntry stores cached max page info
type maxPageCacheEntry struct {
	MaxPage   int
	Timestamp time.Time
}

// Comic represents a JM comic
type Comic struct {
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	Author      string    `json:"author"`
	Description string    `json:"description"`
	Tags        []string  `json:"tags"`
	Pages       int       `json:"pages"`
	Chapters    []Chapter `json:"chapters"`
}

// Chapter represents a comic chapter
type Chapter struct {
	ID               string   `json:"id"`
	Title            string   `json:"title"`
	ScrambleID       string   `json:"scramble_id"`
	ImageURLs        []string `json:"image_urls"`
	ImageNames       []string `json:"image_names"`
	DataOrigDomain   string   `json:"data_orig_domain"`
}

// SearchResult represents search results
type SearchResult struct {
	Comics []Comic `json:"comics"`
	Total  int     `json:"total"`
	Page   int     `json:"page"`
}

// JM Scramble constants (from JMComic-Crawler-Python)
const (
	SCRAMBLE_220980 = 220980
	SCRAMBLE_268850 = 268850
	SCRAMBLE_421926 = 421926 // 2023-02-08 changed image cutting algorithm
)

// Default JM domains
var defaultDomains = []string{
	"18comic.vip",
	"18comic.org",
	"jmcomic.me",
	"jmcomic1.me",
	"jmcomic2.me",
}

// Default image domains
var defaultImgDomains = []string{
	"cdn-msp.jmcomic.org",
	"cdn-msp2.jmcomic.org",
	"cdn-msp.jmapiproxy1.cc",
	"cdn-msp2.jmapiproxy2.cc",
	"cdn-msp.jmapinodeudzn.net",
}

// NewJMClient creates a new JM API client
func NewJMClient(config *Config) *JMClient {
	client := &JMClient{
		config: config,
		httpClient: &http.Client{
			Timeout: 60 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{
					InsecureSkipVerify: true,
				},
				MaxIdleConns:        100,
				MaxIdleConnsPerHost: 20,
				IdleConnTimeout:     90 * time.Second,
			},
		},
		domains:      config.JMDomains,
		maxPageCache: make(map[string]*maxPageCacheEntry),
	}

	// Use default domains if none configured
	if len(client.domains) == 0 {
		client.domains = defaultDomains
	}

	client.baseURL = "https://" + client.domains[0]
	client.imgDomain = defaultImgDomains[0]

	return client
}

// Close closes the client
func (c *JMClient) Close() {
	c.httpClient.CloseIdleConnections()
}

// setHeaders sets common headers for requests
func (c *JMClient) setHeaders(req *http.Request) {
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/121.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/webp,image/apng,*/*;q=0.8")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	req.Header.Set("Connection", "keep-alive")
	req.Header.Set("Referer", c.baseURL+"/")
}

// GetComicDetail gets comic details by ID
func (c *JMClient) GetComicDetail(comicID string) (*Comic, error) {
	// Try each domain until success
	var lastErr error
	for _, domain := range c.domains {
		baseURL := "https://" + domain
		comic, err := c.fetchComicDetail(baseURL, comicID)
		if err == nil {
			return comic, nil
		}
		lastErr = err
	}
	return nil, fmt.Errorf("failed to get comic detail from all domains: %v", lastErr)
}

// fetchComicDetail fetches comic detail from a specific domain
func (c *JMClient) fetchComicDetail(baseURL, comicID string) (*Comic, error) {
	albumURL := fmt.Sprintf("%s/album/%s", baseURL, comicID)

	req, err := http.NewRequest("GET", albumURL, nil)
	if err != nil {
		return nil, err
	}
	c.setHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch comic: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	html := string(body)

	// Parse comic info from HTML
	comic := &Comic{
		ID: comicID,
	}

	// Extract title - pattern: id="book-name">xxx<
	titleRe := regexp.MustCompile(`id="book-name"[^>]*>([\s\S]*?)<`)
	if matches := titleRe.FindStringSubmatch(html); len(matches) > 1 {
		comic.Title = strings.TrimSpace(matches[1])
	}
	
	// Alternative title pattern
	if comic.Title == "" {
		titleRe2 := regexp.MustCompile(`<title>([^<]+)</title>`)
		if matches := titleRe2.FindStringSubmatch(html); len(matches) > 1 {
			title := strings.TrimSpace(matches[1])
			// Remove suffix like " - 禁漫天堂"
			if idx := strings.Index(title, " - "); idx > 0 {
				title = title[:idx]
			}
			if idx := strings.Index(title, " | "); idx > 0 {
				title = title[:idx]
			}
			comic.Title = title
		}
	}

	if comic.Title == "" {
		comic.Title = fmt.Sprintf("Comic %s", comicID)
	}

	// Extract author - pattern: data-type="author">...</span>
	authorRe := regexp.MustCompile(`<span itemprop="author" data-type="author">([\s\S]*?)</span>`)
	if matches := authorRe.FindStringSubmatch(html); len(matches) > 1 {
		authorContent := matches[1]
		// Extract author name from <a> tag
		aTagRe := regexp.MustCompile(`<a[^>]*>\s*(\S+)\s*</a>`)
		if aMatches := aTagRe.FindStringSubmatch(authorContent); len(aMatches) > 1 {
			comic.Author = strings.TrimSpace(aMatches[1])
		}
	}

	// Extract tags - pattern: data-type="tags">...</span>
	tagRe := regexp.MustCompile(`<span itemprop="genre" data-type="tags">([\s\S]*?)</span>`)
	if matches := tagRe.FindStringSubmatch(html); len(matches) > 1 {
		tagContent := matches[1]
		aTagRe := regexp.MustCompile(`<a[^>]*>\s*(\S+)\s*</a>`)
		tagMatches := aTagRe.FindAllStringSubmatch(tagContent, -1)
		for _, match := range tagMatches {
			if len(match) > 1 {
				tag := strings.TrimSpace(match[1])
				if tag != "" {
					comic.Tags = append(comic.Tags, tag)
				}
			}
		}
	}

	// Extract scramble_id from album page
	scrambleRe := regexp.MustCompile(`var scramble_id = (\d+);`)
	albumScrambleID := ""
	if matches := scrambleRe.FindStringSubmatch(html); len(matches) > 1 {
		albumScrambleID = matches[1]
	}

	// Extract chapters (photo IDs) - pattern: data-album="xxx"
	episodeRe := regexp.MustCompile(`data-album="(\d+)"[^>]*>[\s\S]*?第(\d+)[话話]`)
	episodeMatches := episodeRe.FindAllStringSubmatch(html, -1)

	photoIDs := make([]string, 0)
	
	if len(episodeMatches) > 0 {
		// Multi-chapter comic
		seen := make(map[string]bool)
		for _, match := range episodeMatches {
			if len(match) > 1 {
				pid := match[1]
				if !seen[pid] {
					seen[pid] = true
					photoIDs = append(photoIDs, pid)
				}
			}
		}
	} else {
		// Single chapter comic - use album ID
		photoIDs = append(photoIDs, comicID)
	}

	// Sort photo IDs
	sort.Slice(photoIDs, func(i, j int) bool {
		pi, _ := strconv.Atoi(photoIDs[i])
		pj, _ := strconv.Atoi(photoIDs[j])
		return pi < pj
	})

	// Fetch each chapter
	chapters := []Chapter{}
	for i, photoID := range photoIDs {
		chapter, err := c.getChapterImages(baseURL, photoID, albumScrambleID)
		if err != nil {
			continue // Skip failed chapters
		}
		chapter.Title = fmt.Sprintf("Chapter %d", i+1)
		chapters = append(chapters, *chapter)
	}

	if len(chapters) == 0 {
		return nil, fmt.Errorf("no chapters found for comic %s", comicID)
	}

	comic.Chapters = chapters

	// Calculate total pages
	for _, ch := range chapters {
		comic.Pages += len(ch.ImageURLs)
	}

	return comic, nil
}

// getChapterImages fetches image URLs for a chapter
func (c *JMClient) getChapterImages(baseURL, photoID, defaultScrambleID string) (*Chapter, error) {
	photoURL := fmt.Sprintf("%s/photo/%s", baseURL, photoID)

	req, err := http.NewRequest("GET", photoURL, nil)
	if err != nil {
		return nil, err
	}
	c.setHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("photo page returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	html := string(body)
	chapter := &Chapter{
		ID: photoID,
	}

	// Extract scramble_id - pattern: var scramble_id = xxx;
	scrambleRe := regexp.MustCompile(`var scramble_id = (\d+);`)
	if matches := scrambleRe.FindStringSubmatch(html); len(matches) > 1 {
		chapter.ScrambleID = matches[1]
	} else if defaultScrambleID != "" {
		chapter.ScrambleID = defaultScrambleID
	} else {
		chapter.ScrambleID = strconv.Itoa(SCRAMBLE_220980)
	}

	// Extract data_original_domain - pattern: src="https://xxx/media/albums/blank
	domainRe := regexp.MustCompile(`src="https://(.*?)/media/albums/blank`)
	if matches := domainRe.FindStringSubmatch(html); len(matches) > 1 {
		chapter.DataOrigDomain = matches[1]
	} else {
		// Try to find from image tags
		imgDomainRe := regexp.MustCompile(`data-original="https://([\w.-]+)/media/photos/`)
		if matches := imgDomainRe.FindStringSubmatch(html); len(matches) > 1 {
			chapter.DataOrigDomain = matches[1]
		} else {
			// Use default
			chapter.DataOrigDomain = defaultImgDomains[0]
		}
	}

	// Extract page_arr - pattern: var page_arr = [...];
	pageArrRe := regexp.MustCompile(`var page_arr = (\[.*?\]);`)
	if matches := pageArrRe.FindStringSubmatch(html); len(matches) > 1 {
		var pageArr []string
		if err := json.Unmarshal([]byte(matches[1]), &pageArr); err == nil {
			chapter.ImageNames = pageArr
		}
	}

	// If page_arr not found, extract from data-original attributes
	if len(chapter.ImageNames) == 0 {
		imgRe := regexp.MustCompile(`data-original="[^"]*?/media/photos/\d+/([^"?]+)`)
		imgMatches := imgRe.FindAllStringSubmatch(html, -1)
		seen := make(map[string]bool)
		for _, match := range imgMatches {
			if len(match) > 1 {
				imgName := match[1]
				if !seen[imgName] {
					seen[imgName] = true
					chapter.ImageNames = append(chapter.ImageNames, imgName)
				}
			}
		}
	}

	// Sort images by filename number
	sort.Slice(chapter.ImageNames, func(i, j int) bool {
		return extractImageNum(chapter.ImageNames[i]) < extractImageNum(chapter.ImageNames[j])
	})

	// Build full image URLs
	for _, imgName := range chapter.ImageNames {
		imgURL := fmt.Sprintf("https://%s/media/photos/%s/%s", chapter.DataOrigDomain, photoID, imgName)
		chapter.ImageURLs = append(chapter.ImageURLs, imgURL)
	}

	return chapter, nil
}

// extractImageNum extracts the image number from filename for sorting
func extractImageNum(filename string) int {
	re := regexp.MustCompile(`(\d+)\.(?:jpg|jpeg|png|webp|gif)`)
	if matches := re.FindStringSubmatch(filename); len(matches) > 1 {
		if num, err := strconv.Atoi(matches[1]); err == nil {
			return num
		}
	}
	return 0
}

// SearchComics searches for comics
func (c *JMClient) SearchComics(query string, page int) ([]Comic, error) {
	searchURL := fmt.Sprintf("%s/search/photos?search_query=%s&page=%d",
		c.baseURL, url.QueryEscape(query), page)

	req, err := http.NewRequest("GET", searchURL, nil)
	if err != nil {
		return nil, err
	}
	c.setHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("search failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("search returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	html := string(body)
	results := []Comic{}

	// Parse search results - look for album cards
	// Pattern: <a href="/album/123456"...><span>Title</span>
	albumRe := regexp.MustCompile(`<a[^>]+href="/album/(\d+)"[^>]*>[\s\S]*?<span[^>]*>([^<]+)</span>`)
	albumMatches := albumRe.FindAllStringSubmatch(html, -1)

	seen := make(map[string]bool)
	for _, match := range albumMatches {
		if len(match) > 2 {
			id := match[1]
			title := strings.TrimSpace(match[2])
			if !seen[id] && title != "" && len(title) > 1 {
				seen[id] = true
				results = append(results, Comic{
					ID:    id,
					Title: title,
				})
			}
		}
	}

	// Alternative pattern for different HTML structure
	if len(results) == 0 {
		altRe := regexp.MustCompile(`/album/(\d+)[^>]*>[\s\S]*?<[^>]+>([^<]{3,})</`)
		altMatches := altRe.FindAllStringSubmatch(html, -1)
		for _, match := range altMatches {
			if len(match) > 2 {
				id := match[1]
				title := strings.TrimSpace(match[2])
				if !seen[id] && title != "" && len(title) > 2 {
					seen[id] = true
					results = append(results, Comic{
						ID:    id,
						Title: title,
					})
				}
			}
		}
	}

	return results, nil
}

// GetRandomComic gets a random comic
func (c *JMClient) GetRandomComic(query string) (*Comic, error) {
	// Get max page for this query
	maxPage, err := c.GetMaxPage(query)
	if err != nil || maxPage == 0 {
		maxPage = 100 // Default fallback
	}

	// Random page
	page := rand.Intn(maxPage) + 1

	// Search
	results, err := c.SearchComics(query, page)
	if err != nil {
		return nil, err
	}

	if len(results) == 0 {
		// Try first page as fallback
		results, err = c.SearchComics(query, 1)
		if err != nil {
			return nil, err
		}
		if len(results) == 0 {
			return nil, fmt.Errorf("no comics found")
		}
	}

	// Random comic from results
	idx := rand.Intn(len(results))
	return &results[idx], nil
}

// GetMaxPage gets the maximum page number for a search query
func (c *JMClient) GetMaxPage(query string) (int, error) {
	c.mu.RLock()
	if entry, ok := c.maxPageCache[query]; ok {
		// Cache valid for 24 hours
		if time.Since(entry.Timestamp) < 24*time.Hour {
			c.mu.RUnlock()
			return entry.MaxPage, nil
		}
	}
	c.mu.RUnlock()

	// Get first page to verify query works
	results, err := c.SearchComics(query, 1)
	if err != nil {
		return 0, err
	}
	if len(results) == 0 {
		return 0, nil
	}

	// Binary search for max page
	low, high := 1, 3000
	for low < high {
		mid := (low + high + 1) / 2
		midResults, err := c.SearchComics(query, mid)
		if err != nil || len(midResults) == 0 {
			high = mid - 1
		} else {
			low = mid
		}
	}

	maxPage := low

	// Cache the result
	c.mu.Lock()
	c.maxPageCache[query] = &maxPageCacheEntry{
		MaxPage:   maxPage,
		Timestamp: time.Now(),
	}
	c.mu.Unlock()

	return maxPage, nil
}

// CheckDomains checks which domains are available
func (c *JMClient) CheckDomains() (map[string]string, error) {
	results := make(map[string]string)
	var wg sync.WaitGroup
	var mu sync.Mutex

	// Check main domains
	allDomains := append([]string{}, defaultDomains...)

	for _, domain := range allDomains {
		wg.Add(1)
		go func(d string) {
			defer wg.Done()

			testURL := fmt.Sprintf("https://%s", d)
			req, err := http.NewRequest("GET", testURL, nil)
			if err != nil {
				mu.Lock()
				results[d] = "fail"
				mu.Unlock()
				return
			}

			c.setHeaders(req)

			client := &http.Client{
				Timeout: 10 * time.Second,
				Transport: &http.Transport{
					TLSClientConfig: &tls.Config{
						InsecureSkipVerify: true,
					},
				},
			}

			resp, err := client.Do(req)
			if err != nil {
				mu.Lock()
				results[d] = "fail"
				mu.Unlock()
				return
			}
			resp.Body.Close()

			mu.Lock()
			if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusFound {
				results[d] = "ok"
			} else {
				results[d] = "fail"
			}
			mu.Unlock()
		}(domain)
	}

	// Also check from GitHub pages for more domains
	wg.Add(1)
	go func() {
		defer wg.Done()
		moreDomains := c.fetchDomainsFromGitHub()
		for _, d := range moreDomains {
			if _, exists := results[d]; !exists {
				wg.Add(1)
				go func(domain string) {
					defer wg.Done()
					status := c.testDomain(domain)
					mu.Lock()
					results[domain] = status
					mu.Unlock()
				}(d)
			}
		}
	}()

	wg.Wait()
	return results, nil
}

// fetchDomainsFromGitHub fetches additional domains from GitHub pages
func (c *JMClient) fetchDomainsFromGitHub() []string {
	domains := []string{}
	template := "https://jmcmomic.github.io/go/%d.html"

	for i := 300; i <= 308; i++ {
		pageURL := fmt.Sprintf(template, i)
		req, _ := http.NewRequest("GET", pageURL, nil)

		client := &http.Client{
			Timeout: 5 * time.Second,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}

		resp, err := client.Do(req)
		if err != nil {
			continue
		}

		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		// Parse domains from page
		domainRe := regexp.MustCompile(`(?:https?://)?([a-zA-Z0-9][a-zA-Z0-9-]*\.(?:vip|org|me|work|xyz|monster|cc|net))`)
		matches := domainRe.FindAllStringSubmatch(string(body), -1)
		for _, match := range matches {
			if len(match) > 1 {
				domain := match[1]
				if !strings.HasPrefix(domain, "jm365.work") {
					domains = append(domains, domain)
				}
			}
		}
	}

	return domains
}

// testDomain tests if a domain is accessible
func (c *JMClient) testDomain(domain string) string {
	testURL := fmt.Sprintf("https://%s", domain)
	req, err := http.NewRequest("GET", testURL, nil)
	if err != nil {
		return "fail"
	}
	c.setHeaders(req)

	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true,
			},
		},
	}

	resp, err := client.Do(req)
	if err != nil {
		return "fail"
	}
	resp.Body.Close()

	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusFound {
		return "ok"
	}
	return "fail"
}

// UpdateDomains updates available domains
func (c *JMClient) UpdateDomains(domains []string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.domains = domains
	if len(domains) > 0 {
		c.baseURL = "https://" + domains[0]
	}

	// Save to config
	c.config.JMDomains = domains
	configPath := "plugins-config/showmejm/config.json"
	c.config.Save(configPath)
}

// ClearDomains clears configured domains
func (c *JMClient) ClearDomains() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.domains = defaultDomains
	c.baseURL = "https://" + c.domains[0]

	// Clear from config
	c.config.JMDomains = []string{}
	configPath := "plugins-config/showmejm/config.json"
	c.config.Save(configPath)
}

// DownloadImage downloads an image from URL
func (c *JMClient) DownloadImage(imageURL string) ([]byte, error) {
	req, err := http.NewRequest("GET", imageURL, nil)
	if err != nil {
		return nil, err
	}
	c.setHeaders(req)
	req.Header.Set("Accept", "image/webp,image/apng,image/*,*/*;q=0.8")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to download image: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("image download returned status %d for URL %s", resp.StatusCode, imageURL)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	return data, nil
}

// GetScrambleNum calculates the scramble number for image decoding
// This is the core algorithm from JMComic-Crawler-Python
func (c *JMClient) GetScrambleNum(scrambleID string, photoID string, filename string) int {
	scrambleIDInt, _ := strconv.Atoi(scrambleID)
	aid, _ := strconv.Atoi(photoID)

	if aid < scrambleIDInt {
		return 0
	} else if aid < SCRAMBLE_268850 {
		return 10
	} else {
		// New algorithm after SCRAMBLE_421926
		x := 10
		if aid >= SCRAMBLE_421926 {
			x = 8
		}
		
		// MD5 hash based calculation
		s := fmt.Sprintf("%d%s", aid, filename)
		hash := md5.Sum([]byte(s))
		hashHex := hex.EncodeToString(hash[:])
		
		// Get last character's ASCII value
		lastChar := hashHex[len(hashHex)-1]
		num := int(lastChar)
		num = num % x
		num = num*2 + 2
		
		return num
	}
}

// DecodeScrambledImage decodes JM's scrambled images
// JM uses a segmentation-based scrambling algorithm
func (c *JMClient) DecodeScrambledImage(data []byte, chapter *Chapter, filename string) ([]byte, error) {
	// Parse the image
	img, format, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		// If decode fails, return original data (might not be scrambled)
		return data, nil
	}

	bounds := img.Bounds()
	width := bounds.Dx()
	height := bounds.Dy()

	// Calculate scramble number
	scrambleNum := c.GetScrambleNum(chapter.ScrambleID, chapter.ID, filename)
	if scrambleNum == 0 {
		// No scrambling needed
		return data, nil
	}

	// Calculate segment height
	remainder := height % scrambleNum
	segmentHeight := height / scrambleNum

	// Create new image
	result := image.NewRGBA(bounds)

	// Unscramble segments - reverse the scrambling
	for i := 0; i < scrambleNum; i++ {
		// Calculate source position
		srcY := height - (i+1)*segmentHeight
		srcH := segmentHeight
		
		if i == scrambleNum-1 {
			srcY = 0
			srcH = segmentHeight + remainder
		} else if i == 0 {
			srcY = height - segmentHeight - remainder
		}

		// Calculate destination position
		dstY := i * segmentHeight
		if i > 0 {
			dstY += remainder
		}
		
		dstH := segmentHeight
		if i == 0 {
			dstH += remainder
		}

		// Copy segment
		srcRect := image.Rect(0, srcY, width, srcY+srcH)
		dstRect := image.Rect(0, dstY, width, dstY+dstH)
		draw.Draw(result, dstRect, img, srcRect.Min, draw.Src)
	}

	// Encode result
	var buf bytes.Buffer
	if format == "jpeg" || format == "jpg" {
		err = jpeg.Encode(&buf, result, &jpeg.Options{Quality: 95})
	} else {
		// Default to JPEG
		err = jpeg.Encode(&buf, result, &jpeg.Options{Quality: 95})
	}
	if err != nil {
		return data, nil
	}

	return buf.Bytes(), nil
}

// Helper function to parse JSON
func parseJSON(data []byte, v interface{}) error {
	return json.Unmarshal(data, v)
}

// Helper function to build URL
func buildURL(base string, path string, params map[string]string) string {
	u, _ := url.Parse(base)
	u.Path = path

	if len(params) > 0 {
		q := u.Query()
		for k, v := range params {
			q.Set(k, v)
		}
		u.RawQuery = q.Encode()
	}

	return u.String()
}
