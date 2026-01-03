#!/bin/bash

# ShowMeJM Plugin - GitHub 发布脚本
# 使用方法: ./deploy.sh

set -e

echo "🚀 ShowMeJM Plugin 发布脚本"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

# 检查是否在正确的目录
if [ ! -f "main.go" ]; then
    echo "❌ 错误: 请在 plugin-showmejm 目录下运行此脚本"
    exit 1
fi

# 检查 git 状态
if [ -n "$(git status --porcelain)" ]; then
    echo "⚠️  警告: 有未提交的更改"
    git status --short
    read -p "是否继续? (y/n) " -n 1 -r
    echo
    if [[ ! $REPLY =~ ^[Yy]$ ]]; then
        exit 1
    fi
fi

# 获取当前版本
CURRENT_VERSION=$(git describe --tags --abbrev=0 2>/dev/null || echo "v0.0.0")
echo "📌 当前版本: $CURRENT_VERSION"

# 询问新版本
read -p "请输入新版本号 (例如: v1.0.0): " NEW_VERSION

if [ -z "$NEW_VERSION" ]; then
    echo "❌ 版本号不能为空"
    exit 1
fi

# 验证版本号格式
if [[ ! $NEW_VERSION =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
    echo "❌ 版本号格式错误，应为 vX.Y.Z 格式"
    exit 1
fi

echo ""
echo "📝 准备发布 $NEW_VERSION"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

# 检查 go.mod 中的 replace 指令
if grep -q "^replace" go.mod; then
    echo "⚠️  警告: go.mod 中存在 replace 指令"
    echo "   发布前需要注释掉本地 replace 指令"
    read -p "是否自动注释? (y/n) " -n 1 -r
    echo
    if [[ $REPLY =~ ^[Yy]$ ]]; then
        sed -i.bak 's/^replace/\/\/ replace/' go.mod
        echo "✅ 已注释 replace 指令"
        git add go.mod
        git commit -m "chore: comment out local replace for release $NEW_VERSION" || true
    fi
fi

# 检查是否已设置远程仓库
if ! git remote get-url origin &>/dev/null; then
    echo ""
    echo "📦 未设置远程仓库"
    read -p "请输入 GitHub 仓库 URL (例如: https://github.com/hovanzhang/plugin-showmejm.git): " REPO_URL
    
    if [ -z "$REPO_URL" ]; then
        echo "❌ 仓库 URL 不能为空"
        exit 1
    fi
    
    git remote add origin "$REPO_URL"
    echo "✅ 已添加远程仓库: $REPO_URL"
fi

# 显示远程仓库
REMOTE_URL=$(git remote get-url origin)
echo "📦 远程仓库: $REMOTE_URL"

# 确认发布
echo ""
echo "准备执行以下操作:"
echo "  1. 创建标签: $NEW_VERSION"
echo "  2. 推送到远程仓库"
echo "  3. 触发 GitHub Actions 自动构建"
echo ""
read -p "确认发布? (y/n) " -n 1 -r
echo

if [[ ! $REPLY =~ ^[Yy]$ ]]; then
    echo "❌ 已取消发布"
    exit 1
fi

# 推送主分支
echo ""
echo "📤 推送主分支..."
git push -u origin main || git push -u origin master

# 创建并推送标签
echo "🏷️  创建标签 $NEW_VERSION..."
git tag -a "$NEW_VERSION" -m "Release $NEW_VERSION"

echo "📤 推送标签..."
git push origin "$NEW_VERSION"

echo ""
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "✅ 发布成功!"
echo ""
echo "📋 后续步骤:"
echo "  1. 访问 GitHub Actions 查看构建进度"
echo "     $REMOTE_URL/actions"
echo ""
echo "  2. 构建完成后，在 Releases 页面查看发布"
echo "     $REMOTE_URL/releases"
echo ""
echo "  3. 在 bot-platform 中安装插件:"
echo "     ./botctl install $REMOTE_URL"
echo ""
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
