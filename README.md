# ShowMeJM

基于 [bot-platform](https://github.com/DaikonSushi/bot-platform) 的 QQ 机器人 JM 漫画下载插件。

参考项目:
- [ShowMeJM](https://github.com/Antares-Studio/ShowMeJM) - 原始灵感来源
- [JMComic-Crawler-Python](https://github.com/hect0x7/JMComic-Crawler-Python) - JM API 实现参考

> 🤖 本项目多数代码由 Claude vibe coding 生成，请自行解决网络问题。

## 快速部署

推荐使用 Docker Compose 部署完整环境（支持跨平台）:

```bash
# 1. 克隆 bot-platform
git clone https://github.com/DaikonSushi/bot-platform.git
cd bot-platform

# 2. 配置 config.yaml
cp config.example.yaml config.yaml
vim config.yaml  # 设置管理员 QQ 号等

# 3. 启动服务
docker-compose up -d

# 4. 扫码登录 NapCat
# 访问 http://localhost:6099 扫码登录
```

## 安装插件

服务启动后，在 QQ 中给 Bot 发送以下命令（仅管理员可用）:

```
# 安装 ShowMeJM 插件
/plugin install https://github.com/DaikonSushi/plugin-showmejm

# 启动插件
/plugin start showmejm

# 查看所有插件
/plugin list
```

## 使用方法

```
📚 JM漫画下载助手

🔍 搜索: 查jm [关键词] [页码]
   例: 查jm 鸣潮,+无修正 2

📥 下载: jm [jm号]
   例: jm 114514

🎲 随机: 随机jm [关键词]

🌐 域名管理:
   jm check    - 检测可用域名
   jm domain   - 手动设置域名
   jm clear    - 清除域名配置
```

## 配置说明

配置文件位于 `plugins-config/showmejm/config.json`，主要配置项：

| 配置项 | 说明 | 默认值 |
|--------|------|--------|
| `image_quality` | 图片压缩质量（1-100），0 表示不压缩 | 0 |
| `pdf_max_pages` | 每个 PDF 最大页数 | 200 |
| `pdf_password` | PDF 加密密码（留空表示不加密） | "" |
| `cleanup_after` | 生成 PDF 后是否删除原图 | false |
| `concurrent_download` | 最大并发下载数 | 10 |

### 图片压缩说明

`image_quality` 设置图片压缩质量（JPEG 格式）：
- **0**: 不压缩，保持原图质量（文件较大）
- **50-70**: 推荐值，体积减少约 50-70%，画质损失较小
- **80-90**: 高质量压缩，体积减少约 30-50%
- **100**: 最高质量（与原图差异极小）

示例配置：
```json
{
  "image_quality": 70,
  "pdf_max_pages": 200,
  "cleanup_after": true
}
```

## 开发插件

理想情况下，clone [plugin-fileupload](https://github.com/DaikonSushi/plugin-fileupload) 作为模板:

```bash
git clone https://github.com/DaikonSushi/plugin-fileupload.git plugin-myplugin
cd plugin-myplugin

# 修改 go.mod 中的模块名
# 编写你的插件逻辑
# 打 tag 触发 GitHub Actions 自动构建发布
git tag v1.0.0
git push origin v1.0.0
```

## License

MIT
