# MultiPortProxy Sponsors Worker

为 `mpp.shaoyeai.com` 提供 GitHub Sponsors Webhook、公开赞助者 API、D1 持久化和 Telegram 管理员通知。

## 接口

- `GET /api/sponsors`：仅返回公开且未结束的赞助者。
- `POST /webhooks/github/sponsors`：接收 GitHub `sponsorship` Webhook。
- `GET /health`：Worker 健康检查（仅 `workers.dev` 地址可直接访问）。

## 本地开发

```powershell
Copy-Item .dev.vars.example .dev.vars
npm install
npm run types
npx wrangler d1 migrations apply multiportproxy-sponsors --local
npm run dev
```

`.dev.vars` 中使用测试值即可。文件已被仓库根目录的 `.gitignore` 忽略。

## 生产部署

```powershell
npx wrangler d1 migrations apply multiportproxy-sponsors --remote
npx wrangler secret put GITHUB_WEBHOOK_SECRET
npx wrangler secret put TELEGRAM_BOT_TOKEN
npx wrangler secret put TELEGRAM_CHAT_ID
npm run check
npm run deploy
```

如果 Telegram Bot 尚未创建，可以暂时把 `TELEGRAM_BOT_TOKEN` 和 `TELEGRAM_CHAT_ID` 都设置为 `disabled`。Worker 会正常处理赞助事件，但暂不发送通知。

## GitHub Sponsors Webhook

在 GitHub 打开：

`Your sponsors → Dashboard → Webhooks → Add webhook`

填写：

- Payload URL：`https://mpp.shaoyeai.com/webhooks/github/sponsors`
- Content type：`application/json`
- Secret：与 Worker 的 `GITHUB_WEBHOOK_SECRET` 完全一致
- Active：启用

Worker 会验证 `X-Hub-Signature-256`，使用 `X-GitHub-Delivery` 去重，并处理：

- `created`
- `edited`
- `pending_cancellation`
- `pending_tier_change`
- `tier_changed`
- `cancelled`

隐私赞助者不会把用户名、头像或主页保存到 D1，也不会出现在公开 API 中。

## Telegram 通知

1. 通过 `@BotFather` 创建 Bot，得到 Token。
2. 先向 Bot 私聊发送一条消息。
3. 请求 `https://api.telegram.org/bot<TOKEN>/getUpdates`，读取 `message.chat.id`。
4. 分别写入 `TELEGRAM_BOT_TOKEN` 与 `TELEGRAM_CHAT_ID`。

通知只发给管理员私聊，不在公开群中广播取消或档位变化。
