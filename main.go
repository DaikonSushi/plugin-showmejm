// plugin-showmejm - A complete JM comic plugin for bot-platform
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

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
		Version:           "3.3.5",
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

	bot.Log("info", fmt.Sprintf("ShowMeJM plugin v3.3.5 started successfully (max concurrent tasks=%d)", slots))
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
	if !p.config.AutoFindJM {
		return false
	}

	comicID, ok := p.autoFindComicID(msg.Text)
	if !ok {
		return false
	}

	if !p.checkWhitelist(msg) {
		p.replyUnauthorized(bot, msg)
		return p.config.PreventDefault
	}

	go p.downloadComic(ctx, bot, msg, comicID)
	return p.config.PreventDefault
}

// OnCommand handles registered commands
func (p *ShowMeJMPlugin) OnCommand(ctx context.Context, bot *pluginsdk.BotClient, cmd string, args []string, msg *pluginsdk.Message) bool {
	// Check whitelist
	if !p.checkWhitelist(msg) {
		p.replyUnauthorized(bot, msg)
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
		case "allow", "add", "授权", "添加权限":
			p.updateWhitelist(bot, msg, args[1:], true)
			return true
		case "deny", "remove", "del", "删除权限", "移除权限", "取消授权":
			p.updateWhitelist(bot, msg, args[1:], false)
			return true
		case "list", "权限", "白名单", "whitelist":
			p.showWhitelist(bot, msg)
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
	if p.isAdmin(msg) {
		return true
	}

	if msg.Type == "group" {
		if len(p.config.GroupWhitelist) == 0 {
			return true
		}
		return containsID(p.config.GroupWhitelist, msg.GroupID)
	}

	if len(p.config.PersonWhitelist) == 0 {
		return true
	}
	return containsID(p.config.PersonWhitelist, msg.UserID)
}

func (p *ShowMeJMPlugin) autoFindComicID(rawText string) (string, bool) {
	text := rawText
	// Remove CQ codes (e.g., [CQ:face,id=344,...]) to avoid false triggers from emojis/images
	text = regexp.MustCompile(`\[CQ:[^\]]+\]`).ReplaceAllString(text, "")
	// Remove HTML entities (e.g., &#91; &#93;)
	text = regexp.MustCompile(`&#\d+;`).ReplaceAllString(text, "")
	// Remove @ mentions
	text = regexp.MustCompile(`@\S+\s*`).ReplaceAllString(text, "")

	text = strings.TrimSpace(text)
	if text == "" {
		return "", false
	}

	numbers := regexp.MustCompile(`\d+`).FindAllString(text, -1)
	if len(numbers) == 0 {
		return "", false
	}

	concatenated := strings.Join(numbers, "")
	return concatenated, len(concatenated) >= 6 && len(concatenated) <= 7
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
格式: 随机jm [关键词(可选)]`

	if p.isAdmin(msg) {
		helpText += `

4.🌐 域名管理:
- jm check / jm更新域名 - 自动检测可用域名
- jm domain <域名> - 手动设置域名
- jm clear / jm清空域名 - 清除自定义域名

5.🛡️ 权限管理:
- jm allow <QQ号> - 添加账号权限
- jm deny <QQ号> - 移除账号权限
- jm allow group <群号> - 添加群权限
- jm deny group <群号> - 移除群权限
- jm list - 查看当前权限

🛡️ 当前权限:
账号白名单: ` + formatIDList(p.config.PersonWhitelist) + `
群白名单: ` + formatIDList(p.config.GroupWhitelist) + `
管理员: ` + formatIDList(p.config.AdminUsers)
	}

	if p.config.PDFPassword != "" {
		helpText += "\n\n🔐 PDF密码：" + p.config.PDFPassword
	}

	bot.Reply(msg, pluginsdk.Text(helpText))
}

// downloadComic downloads a comic by ID
func (p *ShowMeJMPlugin) downloadComic(ctx context.Context, bot *pluginsdk.BotClient, msg *pluginsdk.Message, comicID string) {
	comicID, ok := cleanComicID(comicID)
	if !ok {
		bot.Reply(msg, pluginsdk.Text("📝 请输入数字JM号，例如: jm 114514"))
		return
	}

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
	if !p.isAdmin(msg) && p.config.MaxPagesWithoutAdmin > 0 && comic.Pages > p.config.MaxPagesWithoutAdmin {
		bot.Reply(msg, pluginsdk.Text(fmt.Sprintf(
			"⚠️ JM%s 共 %d 页，超过当前下载上限 %d 页。\n请联系管理员下载：%s",
			comic.ID,
			comic.Pages,
			p.config.MaxPagesWithoutAdmin,
			p.adminContacts(),
		)))
		return
	}

	downloadDir := filepath.Join(p.config.BaseDir, comic.ID)
	bot.Reply(msg, pluginsdk.Text(fmt.Sprintf("📖 找到漫画: %s\n📄 共 %d 页，正在下载中...", comic.Title, comic.Pages)))
	bot.Log("info", fmt.Sprintf("showmejm download directory: comic=%s path=%s", comic.ID, downloadDir))

	// Download images
	downloader := NewDownloader(p.client, p.config)
	images, err := downloader.DownloadComic(comic)
	if err != nil {
		bot.Reply(msg, pluginsdk.Text(fmt.Sprintf("❌ 下载图片失败: %v\n📁 已下载内容保留在服务器: %s", err, downloadDir)))
		return
	}
	bot.Log("info", fmt.Sprintf("showmejm images ready: comic=%s count=%d dir=%s", comic.ID, len(images), downloadDir))

	// Create PDF
	pdfGen := NewPDFGenerator(p.config)
	pdfFiles, err := pdfGen.CreatePDF(comic, images)
	if err != nil {
		bot.Reply(msg, pluginsdk.Text(fmt.Sprintf("❌ 创建PDF失败: %v\n📁 图片保留在服务器: %s", err, downloadDir)))
		return
	}
	bot.Reply(msg, pluginsdk.Text(fmt.Sprintf("✅ PDF已生成，正在上传到%s...\n📁 服务器路径:\n%s", p.uploadTargetName(msg), formatPathList(pdfFiles))))
	bot.Log("info", fmt.Sprintf("showmejm PDFs generated: comic=%s files=%s", comic.ID, strings.Join(pdfFiles, ", ")))

	// Upload files using BotClient
	baseFileName := p.safeComicFileName(comic)
	uploadedCount := 0
	failedFiles := []string{}
	for i, pdfPath := range pdfFiles {
		// Check file exists and has size
		info, err := os.Stat(pdfPath)
		if err != nil {
			bot.Log("error", fmt.Sprintf("PDF file not found: %s", pdfPath))
			failedFiles = append(failedFiles, pdfPath)
			continue
		}

		fileName := fmt.Sprintf("%s - part%d.pdf", baseFileName, i+1)
		if len(pdfFiles) == 1 {
			fileName = fmt.Sprintf("%s.pdf", baseFileName)
		}

		bot.Log("info", fmt.Sprintf("Uploading PDF: %s (%d bytes)", fileName, info.Size()))

		uploadErr := p.uploadPDF(bot, msg, pdfPath, fileName)
		if uploadErr != nil {
			failedFiles = append(failedFiles, pdfPath)
			bot.Log("error", fmt.Sprintf("showmejm upload failed: comic=%s file=%s path=%s error=%v", comic.ID, fileName, pdfPath, uploadErr))
		} else {
			uploadedCount++
			bot.Log("info", fmt.Sprintf("Uploaded: %s", fileName))
		}
	}

	if len(failedFiles) > 0 {
		bot.Reply(msg, pluginsdk.Text(fmt.Sprintf(
			"⚠️ 上传完成但有 %d/%d 个文件失败。\n✅ 已上传: %d 个\n📁 失败文件保留在服务器:\n%s\n请管理员从服务器取文件或稍后重试。",
			len(failedFiles),
			len(pdfFiles),
			uploadedCount,
			formatPathList(failedFiles),
		)))
		return
	}

	bot.Reply(msg, pluginsdk.Text(fmt.Sprintf("✅ JM%s 处理完成，%d 个PDF已上传到%s。", comic.ID, uploadedCount, p.uploadTargetName(msg))))

	// Cleanup if configured
	// downloader.CleanupDownload(comic)
}

func (p *ShowMeJMPlugin) uploadTargetName(msg *pluginsdk.Message) string {
	if msg.Type == "group" {
		return "群文件根目录"
	}
	return "私聊文件"
}

func cleanComicID(input string) (string, bool) {
	id := strings.TrimSpace(input)
	id = strings.TrimPrefix(strings.ToUpper(id), "JM")
	if id == "" || !regexp.MustCompile(`^\d+$`).MatchString(id) {
		return "", false
	}
	return id, true
}

func formatPathList(paths []string) string {
	if len(paths) == 0 {
		return "无"
	}
	return strings.Join(paths, "\n")
}

func (p *ShowMeJMPlugin) isAdmin(msg *pluginsdk.Message) bool {
	return msg.IsAdmin || p.config.IsPluginAdmin(msg.UserID)
}

func (p *ShowMeJMPlugin) adminContacts() string {
	if len(p.config.AdminUsers) == 0 {
		return "未配置"
	}
	ids := make([]string, 0, len(p.config.AdminUsers))
	for _, id := range p.config.AdminUsers {
		ids = append(ids, strconv.FormatInt(id, 10))
	}
	return strings.Join(ids, ", ")
}

func (p *ShowMeJMPlugin) replyUnauthorized(bot *pluginsdk.BotClient, msg *pluginsdk.Message) {
	bot.Reply(msg, pluginsdk.Text(fmt.Sprintf("抱歉，您没有使用此功能的权限。请向管理员 %s 申请使用权限。", p.adminContacts())))
}

func containsID(ids []int64, target int64) bool {
	for _, id := range ids {
		if id == target {
			return true
		}
	}
	return false
}

func (p *ShowMeJMPlugin) configPath() string {
	return filepath.Join("plugins-config", "showmejm", "config.json")
}

func (p *ShowMeJMPlugin) updateWhitelist(bot *pluginsdk.BotClient, msg *pluginsdk.Message, args []string, allow bool) {
	if !p.isAdmin(msg) {
		bot.Reply(msg, pluginsdk.Text("抱歉，只有管理员可以管理权限"))
		return
	}

	isGroup, id, err := p.parseWhitelistTarget(msg, args)
	if err != nil {
		bot.Reply(msg, pluginsdk.Text("📝 权限命令:\n添加账号: jm allow <QQ号>\n移除账号: jm deny <QQ号>\n添加群: jm allow group <群号>\n移除群: jm deny group <群号>"))
		return
	}

	if allow {
		p.config.AddToWhitelist(isGroup, id)
	} else {
		p.config.RemoveFromWhitelist(isGroup, id)
	}
	if err := p.config.Save(p.configPath()); err != nil {
		bot.Reply(msg, pluginsdk.Text(fmt.Sprintf("❌ 保存权限配置失败: %v", err)))
		return
	}

	targetName := "账号"
	if isGroup {
		targetName = "群"
	}
	action := "添加"
	if !allow {
		action = "移除"
	}
	bot.Reply(msg, pluginsdk.Text(fmt.Sprintf("✅ 已%s%s权限: %d", action, targetName, id)))
}

func (p *ShowMeJMPlugin) parseWhitelistTarget(msg *pluginsdk.Message, args []string) (bool, int64, error) {
	if len(args) == 0 {
		return false, 0, fmt.Errorf("missing target")
	}

	targetType := strings.ToLower(args[0])
	switch targetType {
	case "group", "群", "群聊":
		if len(args) == 1 {
			if msg.Type == "group" {
				return true, msg.GroupID, nil
			}
			return false, 0, fmt.Errorf("missing group id")
		}
		id, err := strconv.ParseInt(args[1], 10, 64)
		return true, id, err
	case "user", "person", "qq", "账号", "用户":
		if len(args) < 2 {
			return false, 0, fmt.Errorf("missing user id")
		}
		id, err := strconv.ParseInt(args[1], 10, 64)
		return false, id, err
	default:
		id, err := strconv.ParseInt(args[0], 10, 64)
		return false, id, err
	}
}

func (p *ShowMeJMPlugin) showWhitelist(bot *pluginsdk.BotClient, msg *pluginsdk.Message) {
	if !p.isAdmin(msg) {
		bot.Reply(msg, pluginsdk.Text("抱歉，只有管理员可以查看权限"))
		return
	}

	bot.Reply(msg, pluginsdk.Text(fmt.Sprintf(
		"🛡️ 当前权限配置\n账号白名单: %s\n群白名单: %s\n管理员: %s",
		formatIDList(p.config.PersonWhitelist),
		formatIDList(p.config.GroupWhitelist),
		formatIDList(p.config.AdminUsers),
	)))
}

func formatIDList(ids []int64) string {
	if len(ids) == 0 {
		return "未配置"
	}
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		parts = append(parts, strconv.FormatInt(id, 10))
	}
	return strings.Join(parts, ", ")
}

func (p *ShowMeJMPlugin) safeComicFileName(comic *Comic) string {
	title := strings.TrimSpace(comic.Title)
	if title == "" {
		title = "Comic"
	}
	name := "JM" + comic.ID + " - " + title
	invalid := regexp.MustCompile(`[\\/:*?"<>|\r\n\t]+`)
	name = invalid.ReplaceAllString(name, " ")
	name = regexp.MustCompile(`\s+`).ReplaceAllString(name, " ")
	name = strings.TrimSpace(name)
	if len([]rune(name)) > 120 {
		runes := []rune(name)
		name = string(runes[:120])
	}
	if name == "" {
		name = "JM" + comic.ID
	}
	return name
}

func (p *ShowMeJMPlugin) uploadPDF(bot *pluginsdk.BotClient, msg *pluginsdk.Message, pdfPath, fileName string) error {
	var lastErr error
	for attempt := 1; attempt <= p.config.UploadRetryCount; attempt++ {
		if msg.Type == "group" {
			lastErr = bot.UploadGroupFile(msg.GroupID, pdfPath, fileName, "/")
		} else {
			lastErr = bot.UploadPrivateFile(msg.UserID, pdfPath, fileName)
		}
		if lastErr == nil {
			return nil
		}
		bot.Log("warn", fmt.Sprintf("Upload attempt %d/%d failed for %s: %v", attempt, p.config.UploadRetryCount, filepath.Base(pdfPath), lastErr))
		if attempt < p.config.UploadRetryCount {
			time.Sleep(p.config.UploadRetryDelay())
		}
	}
	return lastErr
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
