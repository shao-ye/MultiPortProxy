type SponsorshipAction =
  | "created"
  | "edited"
  | "pending_cancellation"
  | "pending_tier_change"
  | "tier_changed"
  | "cancelled";

type GitHubUser = {
  node_id: string;
  login: string;
  avatar_url: string;
  html_url: string;
};

type SponsorshipTier = {
  name: string;
  monthly_price_in_cents: number;
  is_one_time: boolean;
  is_custom_amount: boolean;
};

type SponsorshipPayload = {
  action: SponsorshipAction;
  effective_date?: string;
  sponsorship: {
    node_id: string;
    created_at: string;
    privacy_level: string;
    sponsor: GitHubUser;
    sponsorable: GitHubUser;
    tier: SponsorshipTier;
  };
};

type PublicSponsorRow = {
  sponsor_login: string;
  avatar_url: string;
  profile_url: string;
  tier_name: string;
  is_one_time: number;
  sponsored_at: string;
};

const MAX_WEBHOOK_BYTES = 1024 * 1024;
const encoder = new TextEncoder();

export default {
  async fetch(request: Request, env: Env, ctx: ExecutionContext): Promise<Response> {
    try {
      const url = new URL(request.url);

      if (url.pathname === "/api/sponsors") {
        if (request.method === "OPTIONS") {
          return new Response(null, { status: 204, headers: corsHeaders(request, env) });
        }
        if (request.method !== "GET") {
          return jsonResponse({ error: "Method not allowed" }, 405);
        }
        return await listSponsors(request, env);
      }

      if (url.pathname === "/webhooks/github/sponsors") {
        if (request.method !== "POST") {
          return jsonResponse({ error: "Method not allowed" }, 405);
        }
        return await handleSponsorshipWebhook(request, env, ctx);
      }

      if (url.pathname === "/health") {
        return jsonResponse({ ok: true, service: "multiportproxy-sponsors" });
      }

      return jsonResponse({ error: "Not found" }, 404);
    } catch (error) {
      console.error(JSON.stringify({
        message: "Unhandled request error",
        error: error instanceof Error ? error.message : String(error),
        path: new URL(request.url).pathname,
      }));
      return jsonResponse({ error: "Internal server error" }, 500);
    }
  },
} satisfies ExportedHandler<Env>;

async function listSponsors(request: Request, env: Env): Promise<Response> {
  const result = await env.DB.prepare(`
    SELECT sponsor_login, avatar_url, profile_url, tier_name, is_one_time, sponsored_at
    FROM sponsors
    WHERE privacy_level = 'public'
      AND status != 'cancelled'
      AND sponsor_login IS NOT NULL
    ORDER BY monthly_price_in_cents DESC, sponsored_at ASC
    LIMIT 100
  `).all<PublicSponsorRow>();

  const sponsors = result.results.map((row) => ({
    login: row.sponsor_login,
    avatarUrl: row.avatar_url,
    profileUrl: row.profile_url,
    tierName: row.tier_name,
    isOneTime: row.is_one_time === 1,
    sponsoredAt: row.sponsored_at,
  }));

  const headers = new Headers(corsHeaders(request, env));
  headers.set("Cache-Control", "public, max-age=60, s-maxage=300, stale-while-revalidate=600");
  return jsonResponse({ sponsors, count: sponsors.length }, 200, headers);
}

async function handleSponsorshipWebhook(
  request: Request,
  env: Env,
  ctx: ExecutionContext,
): Promise<Response> {
  const event = request.headers.get("X-GitHub-Event");
  const deliveryId = request.headers.get("X-GitHub-Delivery");
  const signature = request.headers.get("X-Hub-Signature-256");

  if (event !== "sponsorship") {
    return jsonResponse({ error: "Unsupported event" }, 400);
  }
  if (!deliveryId || !signature) {
    return jsonResponse({ error: "Missing GitHub delivery headers" }, 400);
  }

  const declaredLength = Number(request.headers.get("Content-Length") ?? "0");
  if (Number.isFinite(declaredLength) && declaredLength > MAX_WEBHOOK_BYTES) {
    return jsonResponse({ error: "Payload too large" }, 413);
  }

  const rawBody = await request.text();
  if (encoder.encode(rawBody).byteLength > MAX_WEBHOOK_BYTES) {
    return jsonResponse({ error: "Payload too large" }, 413);
  }
  if (!(await verifyGitHubSignature(rawBody, signature, env.GITHUB_WEBHOOK_SECRET))) {
    return jsonResponse({ error: "Invalid signature" }, 401);
  }

  const payload = parseSponsorshipPayload(rawBody);
  if (!payload) {
    return jsonResponse({ error: "Invalid sponsorship payload" }, 400);
  }
  if (payload.sponsorship.sponsorable.login !== env.SPONSORABLE_LOGIN) {
    return jsonResponse({ error: "Unexpected sponsorable account" }, 403);
  }

  const isPublic = payload.sponsorship.privacy_level === "public";
  const status = payload.action === "cancelled" ? "cancelled" : payload.action;
  const now = new Date().toISOString();
  const sponsor = payload.sponsorship.sponsor;
  const tier = payload.sponsorship.tier;

  const [deliveryResult] = await env.DB.batch([
    env.DB.prepare(`
      INSERT OR IGNORE INTO webhook_deliveries (delivery_id, event_action, received_at)
      VALUES (?, ?, ?)
    `).bind(deliveryId, payload.action, now),
    env.DB.prepare(`
      INSERT INTO sponsors (
        sponsorship_id,
        sponsor_node_id,
        sponsor_login,
        avatar_url,
        profile_url,
        privacy_level,
        tier_name,
        monthly_price_in_cents,
        is_one_time,
        is_custom_amount,
        status,
        effective_date,
        sponsored_at,
        updated_at
      )
      VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
      ON CONFLICT(sponsorship_id) DO UPDATE SET
        sponsor_node_id = excluded.sponsor_node_id,
        sponsor_login = excluded.sponsor_login,
        avatar_url = excluded.avatar_url,
        profile_url = excluded.profile_url,
        privacy_level = excluded.privacy_level,
        tier_name = excluded.tier_name,
        monthly_price_in_cents = excluded.monthly_price_in_cents,
        is_one_time = excluded.is_one_time,
        is_custom_amount = excluded.is_custom_amount,
        status = excluded.status,
        effective_date = excluded.effective_date,
        updated_at = excluded.updated_at
    `).bind(
      payload.sponsorship.node_id,
      sponsor.node_id,
      isPublic ? sponsor.login : null,
      isPublic ? sponsor.avatar_url : null,
      isPublic ? sponsor.html_url : null,
      payload.sponsorship.privacy_level,
      tier.name,
      tier.monthly_price_in_cents,
      tier.is_one_time ? 1 : 0,
      tier.is_custom_amount ? 1 : 0,
      status,
      payload.effective_date ?? null,
      payload.sponsorship.created_at,
      now,
    ),
  ]);

  const isNewDelivery = (deliveryResult?.meta.changes ?? 0) > 0;
  if (isNewDelivery && telegramEnabled(env)) {
    ctx.waitUntil(
      sendTelegramNotification(payload, env).catch((error) => {
        console.error(JSON.stringify({
          message: "Telegram notification failed",
          deliveryId,
          error: error instanceof Error ? error.message : String(error),
        }));
      }),
    );
  }

  console.log(JSON.stringify({
    message: isNewDelivery ? "Sponsorship event processed" : "Duplicate sponsorship event ignored",
    deliveryId,
    action: payload.action,
    privacyLevel: payload.sponsorship.privacy_level,
  }));

  return jsonResponse({ ok: true, duplicate: !isNewDelivery });
}

async function verifyGitHubSignature(
  rawBody: string,
  signatureHeader: string,
  secret: string,
): Promise<boolean> {
  if (!signatureHeader.startsWith("sha256=")) {
    return false;
  }

  const supplied = hexToBytes(signatureHeader.slice("sha256=".length));
  if (!supplied) {
    return false;
  }

  const key = await crypto.subtle.importKey(
    "raw",
    encoder.encode(secret),
    { name: "HMAC", hash: "SHA-256" },
    false,
    ["sign"],
  );
  const expected = new Uint8Array(await crypto.subtle.sign("HMAC", key, encoder.encode(rawBody)));

  const suppliedHash = await crypto.subtle.digest("SHA-256", supplied);
  const expectedHash = await crypto.subtle.digest("SHA-256", expected);
  return crypto.subtle.timingSafeEqual(suppliedHash, expectedHash);
}

function hexToBytes(value: string): Uint8Array | null {
  if (!/^[0-9a-f]{64}$/i.test(value)) {
    return null;
  }
  const output = new Uint8Array(value.length / 2);
  for (let index = 0; index < value.length; index += 2) {
    output[index / 2] = Number.parseInt(value.slice(index, index + 2), 16);
  }
  return output;
}

function parseSponsorshipPayload(rawBody: string): SponsorshipPayload | null {
  let value: unknown;
  try {
    value = JSON.parse(rawBody);
  } catch {
    return null;
  }
  if (!isRecord(value) || !isSponsorshipAction(value.action)) {
    return null;
  }
  const sponsorship = value.sponsorship;
  if (!isRecord(sponsorship) || !isGitHubUser(sponsorship.sponsor) || !isGitHubUser(sponsorship.sponsorable)) {
    return null;
  }
  if (
    typeof sponsorship.node_id !== "string"
    || typeof sponsorship.created_at !== "string"
    || typeof sponsorship.privacy_level !== "string"
    || !isSponsorshipTier(sponsorship.tier)
  ) {
    return null;
  }
  if (value.effective_date !== undefined && typeof value.effective_date !== "string") {
    return null;
  }
  return value as SponsorshipPayload;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null;
}

function isSponsorshipAction(value: unknown): value is SponsorshipAction {
  return value === "created"
    || value === "edited"
    || value === "pending_cancellation"
    || value === "pending_tier_change"
    || value === "tier_changed"
    || value === "cancelled";
}

function isGitHubUser(value: unknown): value is GitHubUser {
  return isRecord(value)
    && typeof value.node_id === "string"
    && typeof value.login === "string"
    && typeof value.avatar_url === "string"
    && typeof value.html_url === "string";
}

function isSponsorshipTier(value: unknown): value is SponsorshipTier {
  return isRecord(value)
    && typeof value.name === "string"
    && typeof value.monthly_price_in_cents === "number"
    && typeof value.is_one_time === "boolean"
    && typeof value.is_custom_amount === "boolean";
}

function telegramEnabled(env: Env): boolean {
  return env.TELEGRAM_BOT_TOKEN !== "disabled"
    && env.TELEGRAM_CHAT_ID !== "disabled"
    && env.TELEGRAM_BOT_TOKEN.length > 0
    && env.TELEGRAM_CHAT_ID.length > 0;
}

async function sendTelegramNotification(payload: SponsorshipPayload, env: Env): Promise<void> {
  const sponsor = payload.sponsorship.privacy_level === "public"
    ? `@${escapeHtml(payload.sponsorship.sponsor.login)}`
    : "匿名赞助者（隐私赞助）";
  const tier = escapeHtml(payload.sponsorship.tier.name);
  const amount = (payload.sponsorship.tier.monthly_price_in_cents / 100).toFixed(2);
  const action = sponsorshipActionLabel(payload.action);
  const billing = payload.sponsorship.tier.is_one_time ? "一次性" : "每月";
  const effective = payload.effective_date
    ? `\n生效日期：<code>${escapeHtml(payload.effective_date)}</code>`
    : "";
  const text = [
    "<b>MultiPortProxy · GitHub Sponsors</b>",
    `事件：${action}`,
    `赞助者：${sponsor}`,
    `档位：${tier}`,
    `金额：${billing} $${amount}`,
    `隐私：${escapeHtml(payload.sponsorship.privacy_level)}`,
    effective,
  ].filter(Boolean).join("\n");

  const response = await fetch(`https://api.telegram.org/bot${env.TELEGRAM_BOT_TOKEN}/sendMessage`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      chat_id: env.TELEGRAM_CHAT_ID,
      text,
      parse_mode: "HTML",
      disable_web_page_preview: true,
    }),
  });
  if (!response.ok) {
    const errorBody = await response.text();
    throw new Error(`Telegram API returned ${response.status}: ${errorBody.slice(0, 300)}`);
  }
}

function sponsorshipActionLabel(action: SponsorshipAction): string {
  const labels: Record<SponsorshipAction, string> = {
    created: "新增赞助",
    edited: "隐私设置变更",
    pending_cancellation: "计划取消",
    pending_tier_change: "计划变更档位",
    tier_changed: "赞助档位已变更",
    cancelled: "赞助已结束",
  };
  return labels[action];
}

function escapeHtml(value: string): string {
  return value
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;")
    .replaceAll('"', "&quot;");
}

function corsHeaders(request: Request, env: Env): Headers {
  const headers = new Headers({
    "Access-Control-Allow-Methods": "GET, OPTIONS",
    "Access-Control-Allow-Headers": "Content-Type",
    "Vary": "Origin",
  });
  if (request.headers.get("Origin") === env.PUBLIC_SITE_ORIGIN) {
    headers.set("Access-Control-Allow-Origin", env.PUBLIC_SITE_ORIGIN);
  }
  return headers;
}

function jsonResponse(
  body: unknown,
  status = 200,
  extraHeaders?: Headers,
): Response {
  const headers = extraHeaders ?? new Headers();
  headers.set("Content-Type", "application/json; charset=utf-8");
  headers.set("X-Content-Type-Options", "nosniff");
  return new Response(JSON.stringify(body), { status, headers });
}
