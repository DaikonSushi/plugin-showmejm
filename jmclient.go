package main

import (
	"bytes"
	"crypto/aes"
	"crypto/md5"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/jpeg"
	_ "image/png"
	"io"
	"math/rand"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"path/filepath"
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
	ID             string   `json:"id"`
	Title          string   `json:"title"`
	ScrambleID     string   `json:"scramble_id"`
	ImageURLs      []string `json:"image_urls"`
	ImageNames     []string `json:"image_names"`
	DataOrigDomain string   `json:"data_orig_domain"`
}

// SearchResult represents search results
type SearchResult struct {
	Comics []Comic `json:"comics"`
	Total  int     `json:"total"`
	Page   int     `json:"page"`
}

// JM Scramble constants (from JMComic-Crawler-Python)
const (
	SCRAMBLE_220980          = 220980
	SCRAMBLE_268850          = 268850
	SCRAMBLE_421926          = 421926 // 2023-02-08 changed image cutting algorithm
	APP_VERSION              = "2.0.21"
	APP_TOKEN_SECRET         = "18comicAPP"
	APP_TOKEN_SECRET_CONTENT = "18comicAPPContent"
	APP_DATA_SECRET          = "185Hcomic3PAPP7R"
)

// Default JM domains (updated from https://jmcmomic.github.io/go/)
var defaultDomains = []string{
	"18comic.vip",
	"18comic.ink",
	"jmcomic-zzz.org",
	"jmcomic-zzz.one",
	"jm18c-tcb.cc",
	"jm18c-tcb.club",
	"jm18c-uoi.cc",
	"jm-3x.cc",
}

// Default image domains
var defaultImgDomains = []string{
	"cdn-msp.jmapiproxy1.cc",
	"cdn-msp.jmapiproxy2.cc",
	"cdn-msp2.jmapiproxy2.cc",
	"cdn-msp3.jmapiproxy2.cc",
	"cdn-msp.jmapinodeudzn.net",
	"cdn-msp3.jmapinodeudzn.net",
}

var defaultAPIDomains = []string{
	"www.cdnhjk.net",
	"www.cdngwc.cc",
	"www.cdngwc.net",
	"www.cdngwc.club",
	"www.cdnhjk.cc",
}

// NewJMClient creates a new JM API client
func NewJMClient(config *Config) *JMClient {
	jar, _ := cookiejar.New(nil)
	client := &JMClient{
		config: config,
		httpClient: &http.Client{
			Timeout: 60 * time.Second,
			Jar:     jar,
			Transport: &http.Transport{
				Proxy: http.ProxyFromEnvironment,
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

	comic, err := c.fetchComicDetailFromAPI(comicID)
	if err == nil {
		return comic, nil
	}
	return nil, fmt.Errorf("failed to get comic detail from all domains and app api: html=%v api=%v", lastErr, err)
}

type jmAPIEnvelope struct {
	Code     int             `json:"code"`
	Data     json.RawMessage `json:"data"`
	ErrorMsg string          `json:"errorMsg"`
}

type jmAPIAlbum struct {
	ID          any           `json:"id"`
	Name        string        `json:"name"`
	Author      any           `json:"author"`
	Description string        `json:"description"`
	Tags        []string      `json:"tags"`
	Series      []jmAPISeries `json:"series"`
}

type jmAPISeries struct {
	ID   any    `json:"id"`
	Name string `json:"name"`
	Sort any    `json:"sort"`
}

type jmAPIChapter struct {
	ID       any           `json:"id"`
	Name     string        `json:"name"`
	Images   []string      `json:"images"`
	SeriesID any           `json:"series_id"`
	Series   []jmAPISeries `json:"series"`
}

func (c *JMClient) fetchComicDetailFromAPI(comicID string) (*Comic, error) {
	// The mobile API expects cookies. /setting is enough to seed them in the jar.
	_, _, _ = c.reqJMAPIText("/setting", nil, APP_TOKEN_SECRET)

	var album jmAPIAlbum
	if err := c.reqJMAPI("/album", map[string]string{"id": comicID}, APP_TOKEN_SECRET, APP_DATA_SECRET, &album); err != nil {
		return nil, err
	}

	comic := &Comic{
		ID:          anyToString(album.ID),
		Title:       strings.TrimSpace(album.Name),
		Author:      joinAnyStrings(album.Author),
		Description: album.Description,
		Tags:        album.Tags,
	}
	if comic.ID == "" {
		comic.ID = comicID
	}
	if comic.Title == "" {
		comic.Title = fmt.Sprintf("Comic %s", comicID)
	}

	photoIDs := make([]string, 0, len(album.Series))
	if len(album.Series) == 0 {
		photoIDs = append(photoIDs, comic.ID)
	} else {
		sort.Slice(album.Series, func(i, j int) bool {
			return anyToInt(album.Series[i].Sort) < anyToInt(album.Series[j].Sort)
		})
		for _, series := range album.Series {
			id := anyToString(series.ID)
			if id != "" {
				photoIDs = append(photoIDs, id)
			}
		}
	}

	for i, photoID := range photoIDs {
		chapter, err := c.fetchChapterFromAPI(photoID)
		if err != nil {
			return nil, fmt.Errorf("failed to fetch chapter %s from app api: %w", photoID, err)
		}
		if chapter.Title == "" {
			chapter.Title = fmt.Sprintf("Chapter %d", i+1)
		}
		comic.Pages += len(chapter.ImageURLs)
		comic.Chapters = append(comic.Chapters, *chapter)
	}

	if len(comic.Chapters) == 0 {
		return nil, fmt.Errorf("no chapters found for comic %s from app api", comicID)
	}
	return comic, nil
}

func (c *JMClient) fetchChapterFromAPI(photoID string) (*Chapter, error) {
	var data jmAPIChapter
	if err := c.reqJMAPI("/chapter", map[string]string{"id": photoID}, APP_TOKEN_SECRET, APP_DATA_SECRET, &data); err != nil {
		return nil, err
	}

	scrambleID, err := c.fetchScrambleIDFromAPI(photoID)
	if err != nil || scrambleID == "" {
		scrambleID = strconv.Itoa(SCRAMBLE_220980)
	}

	chapter := &Chapter{
		ID:             anyToString(data.ID),
		Title:          strings.TrimSpace(data.Name),
		ScrambleID:     scrambleID,
		ImageNames:     data.Images,
		DataOrigDomain: defaultImgDomains[0],
	}
	if chapter.ID == "" {
		chapter.ID = photoID
	}
	sort.Slice(chapter.ImageNames, func(i, j int) bool {
		return extractImageNum(chapter.ImageNames[i]) < extractImageNum(chapter.ImageNames[j])
	})
	for _, imgName := range chapter.ImageNames {
		chapter.ImageURLs = append(chapter.ImageURLs, fmt.Sprintf("https://%s/media/photos/%s/%s", chapter.DataOrigDomain, chapter.ID, imgName))
	}
	if len(chapter.ImageURLs) == 0 {
		return nil, fmt.Errorf("no image URLs found for photo %s from app api", photoID)
	}
	return chapter, nil
}

func (c *JMClient) fetchScrambleIDFromAPI(photoID string) (string, error) {
	params := map[string]string{
		"id":            photoID,
		"mode":          "vertical",
		"page":          "0",
		"app_img_shunt": "1",
		"express":       "off",
		"v":             strconv.FormatInt(time.Now().Unix(), 10),
	}
	text, _, err := c.reqJMAPIText("/chapter_view_template", params, APP_TOKEN_SECRET_CONTENT)
	if err != nil {
		return "", err
	}
	re := regexp.MustCompile(`var scramble_id = (\d+);`)
	matches := re.FindStringSubmatch(text)
	if len(matches) < 2 {
		return "", fmt.Errorf("scramble_id not found")
	}
	return matches[1], nil
}

func (c *JMClient) reqJMAPI(path string, params map[string]string, tokenSecret, dataSecret string, out any) error {
	text, ts, err := c.reqJMAPIText(path, params, tokenSecret)
	if err != nil {
		return err
	}

	var envelope jmAPIEnvelope
	if err := json.Unmarshal([]byte(extractJSONObject(text)), &envelope); err != nil {
		return err
	}
	if envelope.Code != http.StatusOK {
		return fmt.Errorf("app api returned code %d: %s", envelope.Code, envelope.ErrorMsg)
	}

	var encoded string
	if err := json.Unmarshal(envelope.Data, &encoded); err != nil {
		return fmt.Errorf("app api data is not encrypted string: %w", err)
	}
	if encoded == "" || encoded == "[]" || encoded == "null" {
		return fmt.Errorf("app api returned empty data")
	}
	decoded, err := decodeJMAPIData(encoded, ts, dataSecret)
	if err != nil {
		return err
	}
	return json.Unmarshal([]byte(decoded), out)
}

func (c *JMClient) reqJMAPIText(path string, params map[string]string, tokenSecret string) (string, string, error) {
	var lastErr error
	for _, domain := range defaultAPIDomains {
		requestParams := copyStringMap(params)
		ts := strconv.FormatInt(time.Now().Unix(), 10)
		apiURL := buildURL("https://"+domain, path, requestParams)
		req, err := http.NewRequest("GET", apiURL, nil)
		if err != nil {
			return "", "", err
		}
		setAPIHeaders(req, ts, tokenSecret)

		resp, err := c.httpClient.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		body, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			lastErr = readErr
			continue
		}
		if resp.StatusCode != http.StatusOK {
			lastErr = fmt.Errorf("app api %s returned status %d", domain, resp.StatusCode)
			continue
		}
		return string(body), ts, nil
	}
	return "", "", lastErr
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

	html := parseJMBase64HTML(string(body))

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

	html := parseJMBase64HTML(string(body))
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
	if len(chapter.ImageURLs) == 0 {
		return nil, fmt.Errorf("no image URLs found for photo %s", photoID)
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
					Proxy: http.ProxyFromEnvironment,
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
					status := c.TestDomain(domain)
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

// TestDomain tests if a domain is accessible
func (c *JMClient) TestDomain(domain string) string {
	testURL := fmt.Sprintf("https://%s", domain)
	req, err := http.NewRequest("GET", testURL, nil)
	if err != nil {
		return "fail"
	}
	c.setHeaders(req)

	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			Proxy: http.ProxyFromEnvironment,
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

// GetCurrentDomain returns the current primary domain
func (c *JMClient) GetCurrentDomain() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if len(c.domains) > 0 {
		return c.domains[0]
	}
	return "(未配置)"
}

// DownloadImage downloads an image from URL, with retry and CDN failover.
// On 5xx / network errors it retries with exponential backoff, and if the
// original CDN host keeps failing it swaps the host for one of the known
// fallback CDNs in defaultImgDomains and tries again.
func (c *JMClient) DownloadImage(imageURL string) ([]byte, error) {
	// Build the ordered list of candidate URLs: original URL first, then the
	// same path rewritten onto each fallback CDN (de-duplicated against the
	// original host).
	candidates := buildImageURLCandidates(imageURL)

	const perURLAttempts = 3
	var lastErr error

	for _, candidate := range candidates {
		for attempt := 1; attempt <= perURLAttempts; attempt++ {
			data, status, err := c.tryDownloadImage(candidate)
			if err == nil {
				return data, nil
			}
			lastErr = err

			// 404 means the resource doesn't exist on this host and will not
			// exist on fallbacks with the same path either — give up early.
			if status == http.StatusNotFound {
				return nil, err
			}

			// For 4xx other than 404 it's usually not worth retrying the same
			// URL; break to try the next candidate CDN.
			if status >= 400 && status < 500 && status != http.StatusTooManyRequests {
				break
			}

			// Back off before the next attempt on the same URL.
			if attempt < perURLAttempts {
				backoff := time.Duration(500*(1<<(attempt-1))) * time.Millisecond
				time.Sleep(backoff)
			}
		}
	}

	return nil, fmt.Errorf("failed to download image after trying %d CDN(s): %w", len(candidates), lastErr)
}

// tryDownloadImage performs a single HTTP GET for an image URL. It returns
// the body bytes on 200 OK, otherwise it returns the HTTP status (0 for
// transport-level errors) together with a descriptive error.
func (c *JMClient) tryDownloadImage(imageURL string) ([]byte, int, error) {
	req, err := http.NewRequest("GET", imageURL, nil)
	if err != nil {
		return nil, 0, err
	}
	c.setHeaders(req)
	req.Header.Set("Accept", "image/webp,image/apng,image/*,*/*;q=0.8")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to download image %s: %w", imageURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, resp.StatusCode, fmt.Errorf("image download returned status %d for URL %s", resp.StatusCode, imageURL)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return data, resp.StatusCode, nil
}

// buildImageURLCandidates returns the original URL first, followed by the
// same path served from each fallback CDN in defaultImgDomains (skipping
// the original host to avoid a redundant retry).
func buildImageURLCandidates(imageURL string) []string {
	parsed, err := url.Parse(imageURL)
	if err != nil || parsed.Host == "" {
		return []string{imageURL}
	}

	candidates := []string{imageURL}
	originalHost := parsed.Host
	for _, host := range defaultImgDomains {
		if host == originalHost {
			continue
		}
		alt := *parsed
		alt.Host = host
		candidates = append(candidates, alt.String())
	}
	return candidates
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

		// Remove file extension from filename (important!)
		// Python: of_file_name(url, trim_suffix=True)
		ext := filepath.Ext(filename)
		filenameWithoutExt := strings.TrimSuffix(filename, ext)

		// MD5 hash based calculation
		s := fmt.Sprintf("%d%s", aid, filenameWithoutExt)
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

	// Create new image for decoded result
	result := image.NewRGBA(bounds)

	// Algorithm from Python JMComic library:
	// over = height % num (remainder)
	// move = floor(height / num) (base segment height)
	over := height % scrambleNum
	move := height / scrambleNum

	for i := 0; i < scrambleNum; i++ {
		// Source Y position in scrambled image (from bottom to top)
		srcY := height - (move * (i + 1)) - over

		// Destination Y position in decoded image (from top to bottom)
		dstY := move * i

		// Segment height for this iteration
		segH := move

		if i == 0 {
			// First segment includes the remainder
			segH += over
		} else {
			// Other segments: destination offset by remainder
			dstY += over
		}

		// Copy segment from source to destination
		for y := 0; y < segH; y++ {
			for x := 0; x < width; x++ {
				result.Set(x, dstY+y, img.At(x, srcY+y))
			}
		}
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

func parseJMBase64HTML(text string) string {
	re := regexp.MustCompile(`const html = base64DecodeUtf8\("(.*?)"\)`)
	matches := re.FindStringSubmatch(text)
	if len(matches) < 2 {
		return text
	}
	decoded, err := base64.StdEncoding.DecodeString(matches[1])
	if err != nil {
		return text
	}
	return string(decoded)
}

func setAPIHeaders(req *http.Request, ts, secret string) {
	token := md5Hex(ts + secret)
	req.Header.Set("User-Agent", "Mozilla/5.0 (Linux; Android 9; V1938CT Build/PQ3A.190705.11211812; wv) AppleWebKit/537.36 (KHTML, like Gecko) Version/4.0 Chrome/91.0.4472.114 Safari/537.36")
	req.Header.Set("Accept", "application/json,text/plain,*/*")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en-US;q=0.8,en;q=0.7")
	req.Header.Set("Connection", "keep-alive")
	req.Header.Set("Token", token)
	req.Header.Set("Tokenparam", ts+","+APP_VERSION)
}

func decodeJMAPIData(encoded, ts, secret string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", err
	}
	key := []byte(md5Hex(ts + secret))
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	if len(raw)%block.BlockSize() != 0 {
		return "", fmt.Errorf("encrypted app api data is not full blocks")
	}
	decoded := make([]byte, len(raw))
	for start := 0; start < len(raw); start += block.BlockSize() {
		block.Decrypt(decoded[start:start+block.BlockSize()], raw[start:start+block.BlockSize()])
	}
	if len(decoded) == 0 {
		return "", fmt.Errorf("empty decrypted app api data")
	}
	pad := int(decoded[len(decoded)-1])
	if pad <= 0 || pad > block.BlockSize() || pad > len(decoded) {
		return "", fmt.Errorf("invalid app api padding")
	}
	return string(decoded[:len(decoded)-pad]), nil
}

func md5Hex(s string) string {
	hash := md5.Sum([]byte(s))
	return hex.EncodeToString(hash[:])
}

func extractJSONObject(text string) string {
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start >= 0 && end >= start {
		return text[start : end+1]
	}
	return text
}

func copyStringMap(in map[string]string) map[string]string {
	if in == nil {
		return map[string]string{}
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func anyToString(v any) string {
	switch x := v.(type) {
	case string:
		return strings.TrimSpace(x)
	case float64:
		return strconv.FormatInt(int64(x), 10)
	case int:
		return strconv.Itoa(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case json.Number:
		return x.String()
	case nil:
		return ""
	default:
		return strings.TrimSpace(fmt.Sprint(v))
	}
}

func anyToInt(v any) int {
	n, _ := strconv.Atoi(anyToString(v))
	return n
}

func joinAnyStrings(v any) string {
	switch x := v.(type) {
	case []string:
		return strings.Join(x, ", ")
	case []any:
		parts := make([]string, 0, len(x))
		for _, item := range x {
			if s := anyToString(item); s != "" {
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, ", ")
	case string:
		return x
	default:
		return anyToString(v)
	}
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
