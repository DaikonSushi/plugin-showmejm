// plugin-showmejm - A complete JM comic plugin for bot-platform
package main

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/DaikonSushi/bot-platform/pkg/pluginsdk"
)

// ShowMeJMPlugin implements the JM comic plugin
type ShowMeJMPlugin struct {
	bot    *pluginsdk.BotClient
	config *Config
	client *JMClient
}

// Info returns plugin metadata
func (p *ShowMeJMPlugin) Info() pluginsdk.PluginInfo {
	return pluginsdk.PluginInfo{
		Name:              "showmejm",
		Version:           "3.1.0",
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

	bot.Log("info", "ShowMeJM plugin v3.1.0 started successfully")
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
		// Remove @ mentions
		text = regexp.MustCompile(`@\S+\s*`).ReplaceAllString(text, "")

		// Find all numbers and concatenate
		numbers := regexp.MustCompile(`\d+`).FindAllString(text, -1)
		if len(numbers) > 0 {
			concatenated := strings.Join(numbers, "")
			if len(concatenated) >= 6 && len(concatenated) <= 7 {
				bot.Reply(msg, pluginsdk.Text(fmt.Sprintf("你提到了%s...对吧?", concatenated)))
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
		go p.downloadComic(ctx, bot, msg, args[0])
		return true

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

4.🌐 寻找可用下载域名:
格式: jm更新域名

5.🗑️ 清除默认域名:
格式: jm清空域名`

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

	bot.Reply(msg, pluginsdk.Text(fmt.Sprintf("📥 即将开始下载 JM%s, 请稍候...", comicID)))

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

	bot.Log("info", fmt.Sprintf("Downloaded %d images", len(images)))
	bot.Reply(msg, pluginsdk.Text(fmt.Sprintf("✅ 已下载 %d 张图片，正在生成PDF...", len(images))))

	// Create PDF
	pdfGen := NewPDFGenerator(p.config)
	pdfFiles, err := pdfGen.CreatePDF(comic, images)
	if err != nil {
		bot.Reply(msg, pluginsdk.Text(fmt.Sprintf("❌ 创建PDF失败: %v", err)))
		return
	}

	bot.Reply(msg, pluginsdk.Text("📤 PDF已打包完成，正在上传..."))

	// Upload files using BotClient
	uploadSuccess := true
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
			uploadSuccess = false
		} else {
			bot.Log("info", fmt.Sprintf("Uploaded: %s", fileName))
		}
	}

	if uploadSuccess {
		bot.Reply(msg, pluginsdk.Text("✅ 上传完成！"))
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

func main() {
	pluginsdk.Run(&ShowMeJMPlugin{})
}
