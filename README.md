# Telegram 客服机器人

用户私聊机器人提问 → 消息转发到客服群 → 客服**回复**那条转发消息 → 回复自动发回给用户。

- 支持文字、图片、文件、语音、贴纸等所有消息类型，格式原样保留（使用 `copyMessage`，不暴露转发来源）
- 客服回复对用户是匿名的，用户看到的是机器人发来的消息
- 对话自动串成回复链：用户回复客服的答复时，群里的转发也会挂在那条客服消息下面
- 客服之间互相回复讨论**不会**发给用户，只有回复机器人转发的用户消息才会发出
- 管理面板全部在 Telegram 里完成：设置客服群组 ID、管理员、黑名单、欢迎语、自动回复
- 单个二进制，数据存在 SQLite（纯 Go，无需 CGO），自带 Dockerfile

## 快速开始

1. 找 [@BotFather](https://t.me/BotFather) 发送 `/newbot` 创建机器人，拿到 token。
2. 复制配置：`cp .env.example .env`，填入 `BOT_TOKEN`。
   不知道自己的用户 ID：先让 `ADMIN_IDS` 留空启动一次，私聊机器人发送 `/id`，再填进 `ADMIN_IDS` 重启。
3. 启动（镜像由 GitHub Actions 自动构建，服务器不需要编译，详见下方[部署与发版](#部署与发版)）：

   ```bash
   docker compose pull && docker compose up -d
   ```

   或者本地直接跑（会自动读取当前目录的 `.env`）：

   ```bash
   go run .
   ```

4. 建一个客服群，把机器人拉进去，然后二选一：
   - 在群里发送 `/bind`（管理员），一键绑定；
   - 或者私聊机器人发送 `/admin` → **📍 设置客服群组** → 直接发送群组 ID（如 `-1001234567890`）。
     群组 ID 可在群里发送 `/id` 查看；只发数字部分（`1234567890`）也能自动识别。

绑定成功后群里会收到一条确认消息，之后就可以开始接待了。

## 使用

**用户**：私聊机器人直接发消息即可。`/start` 显示欢迎语，`/id` 查看自己的 ID。

**客服群**：每条用户消息下方有两个按钮：

| 按钮 | 作用 |
| --- | --- |
| 👤 用户名 | 弹窗显示用户 ID、首次/最近联系时间、消息数、封禁状态 |
| 🚫 封禁 / ✅ 解封 | 封禁需二次确认；被封禁用户的消息不再转发，用户侧无提示 |

回复那条消息 = 回复用户。发送成功后机器人给你的消息点一个 👌（群里禁用了表情回应时改为文字提示），失败会说明原因（例如用户已屏蔽机器人）。

在群里**回复**用户消息时还可以用这些命令：

| 命令 | 作用 |
| --- | --- |
| `/info` | 查看用户信息 |
| `/ban [原因]` | 封禁 |
| `/unban` | 解封 |

客服群里的所有成员都被视为客服，都可以回复和封禁用户，所以只把可信的人拉进群。

**管理员**（私聊机器人）：

| 命令 | 作用 |
| --- | --- |
| `/admin` | 打开管理面板 |
| `/setgroup <群组ID>` | 直接设置客服群组 |
| `/cancel` | 取消当前等待输入的操作 |

面板功能：设置/检测/解除客服群组；添加管理员（发送 ID 或转发对方的一条消息）和移除管理员；黑名单查看与解封；自定义欢迎语、自动回复（支持粗体、链接等格式，可关闭）。

`ADMIN_IDS` 里的是超级管理员，面板里无法移除；在面板里添加的管理员可以随时移除。

## 配置

| 环境变量 | 默认值 | 说明 |
| --- | --- | --- |
| `BOT_TOKEN` | 必填 | BotFather 给的 token |
| `ADMIN_IDS` | 空 | 超级管理员用户 ID，逗号分隔 |
| `DB_PATH` | `data/bot.db` | SQLite 路径（Docker 里为 `/data/bot.db`） |
| `ACK_COOLDOWN` | `10m` | 同一用户多久内只自动回复一次 |
| `LINK_RETENTION_DAYS` | `90` | 消息对应关系保留天数，超期后无法再回复那条旧消息 |
| `LOG_LEVEL` | `info` | 设为 `debug` 输出更多日志 |
| `TELEGRAM_API_BASE` | `https://api.telegram.org` | 可指向自建的 Bot API Server |
| `HTTPS_PROXY` | 空 | 服务器访问不了 Telegram 时使用代理 |

## 部署与发版

[`.github/workflows/release.yml`](.github/workflows/release.yml) 会自动跑测试并构建镜像（linux/amd64 + linux/arm64），推送到 GHCR：

| 触发 | 镜像标签 | 其他产物 |
| --- | --- | --- |
| 推送到 `main` | `latest`、`sha-<短哈希>` | — |
| 推送 `v*` 标签（如 `v0.1.0`） | `0.1.0`、`0.1` | GitHub Release（自动生成更新说明 + 各平台二进制 + SHA256SUMS） |
| Pull Request | 只跑测试 | — |

**发一个正式版本：**

```bash
git tag v0.1.0 && git push origin v0.1.0
```

带 `-` 的标签（如 `v0.2.0-rc1`）会标记为预发布。

**服务器部署：** 只需要 `docker-compose.yml` 和 `.env` 两个文件，不需要源码：

```bash
mkdir tg-support-bot && cd tg-support-bot
curl -fsSLO https://raw.githubusercontent.com/MeowAPI/tg-support-bot/main/docker-compose.yml
curl -fsSL https://raw.githubusercontent.com/MeowAPI/tg-support-bot/main/.env.example -o .env
# 编辑 .env 填入 BOT_TOKEN、ADMIN_IDS
docker compose pull && docker compose up -d
```

以后升级也是 `docker compose pull && docker compose up -d`。

如果 `docker compose pull` 报 `unauthorized`，说明镜像包是私有的：要么在 GitHub 包设置里把 `tg-support-bot` 改为 Public，要么在服务器上先 `docker login ghcr.io -u <GitHub用户名>`（密码用只勾选 `read:packages` 的 Personal access token）。

默认跟随 `latest`（即 main 分支最新构建）。生产环境建议在 `.env` 里写 `TAG=0.1.0` 固定版本，升级时改版本号再执行上面的命令。启动日志第一行会打印当前运行的版本号。

想在本地自己构建镜像：`docker build -t tg-support-bot .`

## 注意事项

- **不需要**关闭机器人的隐私模式（Privacy Mode）：隐私模式下机器人仍能收到“对它消息的回复”。
  但隐私模式下群里不带 `@机器人` 的命令可能收不到，如果 `/bind`、`/id` 没反应，请用 `/bind@你的机器人用户名`，或把机器人设为群管理员。
- 普通群升级为超级群后群 ID 会变，机器人会自动跟随新 ID。
- 机器人被踢出客服群时，会私聊通知所有管理员（前提是管理员私聊过机器人）。
- 如果客服群开启了“限制保存内容”，机器人无法把客服消息复制给用户，请关闭该选项。
- 以匿名管理员身份发言时，`/bind` 无法识别你是机器人管理员，请先关闭匿名。
- 编辑已发送的消息不会同步到对方。
- 同一时间只能运行一个实例（Telegram 的 `getUpdates` 限制）。

## 开发

```bash
go test ./...
```

测试用一个假的 Bot API 服务器跑完整流程（转发、回复、串回复链、面板设置群组、封禁、群升级、用户屏蔽机器人、轮询与退出时确认 offset），不需要真实 token。

```
main.go                     配置加载与启动
internal/telegram/          精简的 Bot API 客户端
internal/store/             SQLite 存储（设置、管理员、黑名单、用户、消息对应关系）
internal/bot/relay.go       用户 ↔ 客服群的转发逻辑
internal/bot/admin.go       管理面板
```
