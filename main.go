// plugin-showmejm - A complete JM comic plugin for bot-platform
package main

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/DaikonSushi/bot-platform/pkg/pluginsdk"
)

// ShowMeJMPlugin implements the JM comic plugin
type ShowMeJMPlugin struct {
	bot    *pluginsdk.BotClient
	config *Config
	client *JMClient

	// Concurrency control
	taskSlots  chan struct{} // Global task-level semaphore (capacity = config.MaxConcurrentTasks)
	activeJobs sync.Map      // key: comicID (string), value: struct{}; prevents duplicate downloads of the same comic
}

// Info returns plugin metadata
func (p *ShowMeJMPlugin) Info() pluginsdk.PluginInfo {
	return pluginsdk.PluginInfo{
		Name:              "showmejm",
		Version:           "3.2.1",
		Description:       "JM comic download and search plugin with full PDF support",
		Author:            "hovanzhang",
		Commands:          []string{"jm", "查jm", "随机jm", "jm更新域名", "jm清空域名"},
		HandleAllMessages: true, // Need to handle auto-find JM numbers
	}
}

// OnStart is called when the plugin starts
func (p *ShowMeJMPlugin) OnStart(bot *pluginsdk.BotClient) error {
	p.bot = bot

	// Load configuration
	config, err := LoadConfig()
	if err != nil {
		bot.Log("error", fmt.Sprintf("Failed to load config: %v", err))
		return err
	}
	p.config = config

	// Initialize JM client
	p.client = NewJMClient(config)

	// Initialize task-level concurrency control
	slots := config.MaxConcurrentTasks
	if slots <= 0 {
		slots = 2
	}
	p.taskSlots = make(chan struct{}, slots)

	bot.Log("info", fmt.Sprintf("ShowMeJM plugin v3.2.1 started successfully (max concurrent tasks=%d)", slots))
	return nil
}

// OnStop is called when the plugin stops
func (p *ShowMeJMPlugin) OnStop() error {
	if p.client != nil {
		p.client.Close()
	}
	return nil
}

// OnMessage handles all incoming messages
func (p *ShowMeJMPlugin) OnMessage(ctx context.Context, bot *pluginsdk.BotClient, msg *pluginsdk.Message) bool {
	// Check whitelist
	if !p.checkWhitelist(msg) {
		return false
	}

	// Auto-find JM numbers in message
	if p.config.AutoFindJM {
		text := msg.Text
		// Remove CQ codes (e.g., [CQ:face,id=344,...]) to avoid false triggers from emojis/images
		text = regexp.MustCompile(`\[CQ:[^\]]+\]`).ReplaceAllString(text, "")
		// Remove HTML entities (e.g., &#91; &#93;)
		text = regexp.MustCompile(`&#\d+;`).ReplaceAllString(text, "")
		// Remove @ mentions
		text = regexp.MustCompile(`@\S+\s*`).ReplaceAllString(text, "")

		// Skip if text is empty after filtering
		text = strings.TrimSpace(text)
		if text == "" {
			return false
		}

		// Find all numbers and concatenate
		numbers := regexp.MustCompile(`\d+`).FindAllString(text, -1)
		if len(numbers) > 0 {
			concatenated := strings.Join(numbers, "")
			if len(concatenated) >= 6 && len(concatenated) <= 7 {
				go p.downloadComic(ctx, bot, msg, concatenated)
				return p.config.PreventDefault
			}
		}
	}
	return false
}

// OnCommand handles registered commands
func (p *ShowMeJMPlugin) OnCommand(ctx context.Context, bot *pluginsdk.BotClient, cmd string, args []string, msg *pluginsdk.Message) bool {
	// Check whitelist
	if !p.checkWhitelist(msg) {
		bot.Reply(msg, pluginsdk.Text("抱歉，您没有使用此功能的权限"))
		return true
	}

	switch {
	case cmd == "jm" && len(args) == 0:
		p.showHelp(bot, msg)
		return true

	case cmd == "jm" && len(args) > 0:
		// Handle subcommands first
		switch strings.ToLower(args[0]) {
		case "help", "帮助":
			p.showHelp(bot, msg)
			return true
		case "domain", "域名":
			if len(args) > 1 {
				go p.setDomain(ctx, bot, msg, args[1])
			} else {
				bot.Reply(msg, pluginsdk.Text("📝 设置域名:\n格式: jm domain <域名>\n例: jm domain 18comic.vip\n\n当前域名: "+p.client.GetCurrentDomain()))
			}
			return true
		case "check", "检查", "检测":
			go p.updateDomains(ctx, bot, msg)
			return true
		case "clear", "清空":
			p.clearDomains(ctx, bot, msg)
			return true
		case "更新域名":
			go p.updateDomains(ctx, bot, msg)
			return true
		default:
			// Treat as comic ID
			go p.downloadComic(ctx, bot, msg, args[0])
			return true
		}

	case strings.HasPrefix(cmd, "查jm"):
		p.searchComic(ctx, bot, msg, args)
		return true

	case strings.HasPrefix(cmd, "随机jm"):
		go p.randomComic(ctx, bot, msg, args)
		return true

	case cmd == "jm更新域名":
		go p.updateDomains(ctx, bot, msg)
		return true

	case cmd == "jm清空域名":
		p.clearDomains(ctx, bot, msg)
		return true
	}

	return false
}

// checkWhitelist checks if user/group is allowed to use the plugin
func (p *ShowMeJMPlugin) checkWhitelist(msg *pluginsdk.Message) bool {
	isGroup := msg.Type == "group"
	var id int64
	if isGroup {
		id = msg.GroupID
	} else {
		id = msg.UserID
	}
	return p.config.CheckWhitelist(isGroup, id)
}

// showHelp displays help information
func (p *ShowMeJMPlugin) showHelp(bot *pluginsdk.BotClient, msg *pluginsdk.Message) {
	helpText := `📚 JM漫画下载助手

1.🔍 搜索功能:
格式: 查jm [关键词/标签] [页码(默认第一页)]
例: 查jm 鸣潮,+无修正 2

2.📥 下载指定id的本子:
格式: jm [jm号]
例: jm 114514

3.🎲 下载随机本子:
格式: 随机jm [关键词(可选)]

4.🌐 域名管理:
- jm check / jm更新域名 - 自动检测可用域名
- jm domain <域名> - 手动设置域名
- jm clear / jm清空域名 - 清除自定义域名`

	if p.config.PDFPassword != "" {
		helpText += "\n\n🔐 PDF密码：" + p.config.PDFPassword
	}

	bot.Reply(msg, pluginsdk.Text(helpText))
}

// downloadComic downloads a comic by ID
func (p *ShowMeJMPlugin) downloadComic(ctx context.Context, bot *pluginsdk.BotClient, msg *pluginsdk.Message, comicID string) {
	// Clean comic ID
	comicID = strings.TrimSpace(comicID)
	comicID = strings.TrimPrefix(strings.ToUpper(comicID), "JM")

	// Gate 1: de-duplicate by comicID — reject if the same comic is already being downloaded.
	if _, loaded := p.activeJobs.LoadOrStore(comicID, struct{}{}); loaded {
		bot.Reply(msg, pluginsdk.Text(fmt.Sprintf("⏳ JM%s 正在下载中，请稍候，完成后会自动发给你~", comicID)))
		return
	}
	defer p.activeJobs.Delete(comicID)

	// Gate 2: global task-level semaphore — bound the number of concurrent tasks across the whole plugin.
	select {
	case p.taskSlots <- struct{}{}:
		defer func() { <-p.taskSlots }()
	default:
		bot.Reply(msg, pluginsdk.Text(fmt.Sprintf("🚦 同时处理的下载任务已达上限(%d)，请稍后再试~", cap(p.taskSlots))))
		return
	}

	// Get comic details
	comic, err := p.client.GetComicDetail(comicID)
	if err != nil {
		bot.Reply(msg, pluginsdk.Text(fmt.Sprintf("❌ 获取漫画信息失败: %v", err)))
		return
	}

	bot.Log("info", fmt.Sprintf("Downloading comic: [%s] %s (%d pages)", comic.ID, comic.Title, comic.Pages))
	bot.Reply(msg, pluginsdk.Text(fmt.Sprintf("📖 找到漫画: %s\n📄 共 %d 页，正在下载中...", comic.Title, comic.Pages)))

	// Download images
	downloader := NewDownloader(p.client, p.config)
	images, err := downloader.DownloadComic(comic)
	if err != nil {
		bot.Reply(msg, pluginsdk.Text(fmt.Sprintf("❌ 下载图片失败: %v", err)))
		return
	}



	// Create PDF
	pdfGen := NewPDFGenerator(p.config)
	pdfFiles, err := pdfGen.CreatePDF(comic, images)
	if err != nil {
		bot.Reply(msg, pluginsdk.Text(fmt.Sprintf("❌ 创建PDF失败: %v", err)))
		return
	}


	// Upload files using BotClient
	
	for i, pdfPath := range pdfFiles {
		// Check file exists and has size
		info, err := os.Stat(pdfPath)
		if err != nil {
			bot.Log("error", fmt.Sprintf("PDF file not found: %s", pdfPath))
			continue
		}

		fileName := fmt.Sprintf("%s-%d.pdf", comic.ID, i+1)
		if len(pdfFiles) == 1 {
			fileName = fmt.Sprintf("%s.pdf", comic.ID)
		}

		bot.Log("info", fmt.Sprintf("Uploading PDF: %s (%d bytes)", fileName, info.Size()))

		var uploadErr error
		if msg.Type == "group" {
			uploadErr = bot.UploadGroupFile(msg.GroupID, pdfPath, fileName, "/")
		} else {
			uploadErr = bot.UploadPrivateFile(msg.UserID, pdfPath, fileName)
		}

		if uploadErr != nil {
			bot.Reply(msg, pluginsdk.Text(fmt.Sprintf("❌ 上传文件失败: %v", uploadErr)))
			bot.Log("error", fmt.Sprintf("Upload failed: %v", uploadErr))
			
		} else {
			bot.Log("info", fmt.Sprintf("Uploaded: %s", fileName))
		}
	}


	// Cleanup if configured
	// downloader.CleanupDownload(comic)
}

// searchComic searches for comics
func (p *ShowMeJMPlugin) searchComic(ctx context.Context, bot *pluginsdk.BotClient, msg *pluginsdk.Message, args []string) {
	if len(args) == 0 {
		bot.Reply(msg, pluginsdk.Text("📝 搜索帮助:\n格式: 查jm [关键词/标签] [页码(默认第一页)]\n例: 查jm 鸣潮,+无修正 2\n提示: 请使用中英文任意逗号隔开每个关键词/标签"))
		return
	}

	query := args[0]
	page := 1
	if len(args) > 1 {
		if n, err := strconv.Atoi(args[1]); err == nil {
			page = n
		}
	}

	// Convert commas to spaces for search
	tags := regexp.MustCompile(`[，,]+`).ReplaceAllString(query, " ")

	bot.Reply(msg, pluginsdk.Text(fmt.Sprintf("🔍 正在搜索: %s (第%d页)...", query, page)))

	results, err := p.client.SearchComics(tags, page)
	if err != nil {
		bot.Reply(msg, pluginsdk.Text(fmt.Sprintf("❌ 搜索失败: %v", err)))
		return
	}

	if len(results) == 0 {
		bot.Reply(msg, pluginsdk.Text("😕 未找到相关漫画"))
		return
	}

	// Format results
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("📚 搜索结果 (第%d页)\n", page))
	sb.WriteString("━━━━━━━━━━━━━━━━\n")
	for i, comic := range results {
		sb.WriteString(fmt.Sprintf("%d. [JM%s] %s\n", i+1, comic.ID, comic.Title))
	}
	sb.WriteString("━━━━━━━━━━━━━━━━\n")
	sb.WriteString("💡 对我说 jm [jm号] 进行下载~")

	bot.Reply(msg, pluginsdk.Text(sb.String()))
}

// randomComic downloads a random comic
func (p *ShowMeJMPlugin) randomComic(ctx context.Context, bot *pluginsdk.BotClient, msg *pluginsdk.Message, args []string) {
	query := ""
	if len(args) > 0 {
		query = args[0]
		query = regexp.MustCompile(`[，,]+`).ReplaceAllString(query, " ")
		bot.Reply(msg, pluginsdk.Text(fmt.Sprintf("🎲 正在搜索关键词为 %s 的随机本子，请稍候...", query)))
	} else {
		bot.Reply(msg, pluginsdk.Text("🎲 正在搜索随机本子，请稍候..."))
	}

	comic, err := p.client.GetRandomComic(query)
	if err != nil {
		bot.Reply(msg, pluginsdk.Text(fmt.Sprintf("❌ 获取随机本子失败: %v", err)))
		return
	}

	bot.Reply(msg, pluginsdk.Text(fmt.Sprintf("🎯 你今天的幸运本子是:\n[JM%s] %s\n\n即将开始下载...", comic.ID, comic.Title)))
	p.downloadComic(ctx, bot, msg, comic.ID)
}

// updateDomains checks and updates available domains
func (p *ShowMeJMPlugin) updateDomains(ctx context.Context, bot *pluginsdk.BotClient, msg *pluginsdk.Message) {
	bot.Reply(msg, pluginsdk.Text("🌐 正在检查域名连接状态，请稍后..."))

	domains, err := p.client.CheckDomains()
	if err != nil {
		bot.Reply(msg, pluginsdk.Text(fmt.Sprintf("❌ 检查域名失败: %v", err)))
		return
	}

	var sb strings.Builder
	sb.WriteString("📊 域名连接状态:\n")
	sb.WriteString("━━━━━━━━━━━━━━━━\n")
	
	usableDomains := []string{}
	for domain, status := range domains {
		icon := "❌"
		if status == "ok" {
			icon = "✅"
			usableDomains = append(usableDomains, domain)
		}
		sb.WriteString(fmt.Sprintf("%s %s\n", icon, domain))
	}

	bot.Reply(msg, pluginsdk.Text(sb.String()))

	if len(usableDomains) > 0 {
		p.client.UpdateDomains(usableDomains)
		bot.Reply(msg, pluginsdk.Text(fmt.Sprintf("✅ 已将 %d 个可用域名添加到配置中\n\n💡 如遇网络问题下载失败，对我说 'jm清空域名' 来清除配置", len(usableDomains))))
	} else {
		bot.Reply(msg, pluginsdk.Text("⚠️ 未找到可用域名，请稍后重试"))
	}
}

// clearDomains clears configured domains
func (p *ShowMeJMPlugin) clearDomains(ctx context.Context, bot *pluginsdk.BotClient, msg *pluginsdk.Message) {
	p.client.ClearDomains()
	bot.Reply(msg, pluginsdk.Text("🗑️ 已清空配置中的域名\n\n💡 插件将自动寻找可用域名\n对我说 'jm更新域名' 可以手动检测并添加可用域名"))
}

// setDomain manually sets a specific domain
func (p *ShowMeJMPlugin) setDomain(ctx context.Context, bot *pluginsdk.BotClient, msg *pluginsdk.Message, domain string) {
	// Clean domain input
	domain = strings.TrimSpace(domain)
	domain = strings.TrimPrefix(domain, "https://")
	domain = strings.TrimPrefix(domain, "http://")
	domain = strings.TrimSuffix(domain, "/")

	bot.Reply(msg, pluginsdk.Text(fmt.Sprintf("🌐 正在测试域名 %s ...", domain)))

	// Test the domain first
	status := p.client.TestDomain(domain)
	if status != "ok" {
		bot.Reply(msg, pluginsdk.Text(fmt.Sprintf("❌ 域名 %s 连接失败，请检查域名是否正确", domain)))
		return
	}

	// Update domain
	p.client.UpdateDomains([]string{domain})
	bot.Reply(msg, pluginsdk.Text(fmt.Sprintf("✅ 已将域名设置为: %s", domain)))
}

func main() {
	pluginsdk.Run(&ShowMeJMPlugin{})
}
