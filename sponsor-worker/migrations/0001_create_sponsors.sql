CREATE TABLE IF NOT EXISTS webhook_deliveries (
  delivery_id TEXT PRIMARY KEY,
  event_action TEXT NOT NULL,
  received_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS sponsors (
  sponsorship_id TEXT PRIMARY KEY,
  sponsor_node_id TEXT NOT NULL,
  sponsor_login TEXT,
  avatar_url TEXT,
  profile_url TEXT,
  privacy_level TEXT NOT NULL,
  tier_name TEXT NOT NULL,
  monthly_price_in_cents INTEGER NOT NULL DEFAULT 0,
  is_one_time INTEGER NOT NULL DEFAULT 0,
  is_custom_amount INTEGER NOT NULL DEFAULT 0,
  status TEXT NOT NULL,
  effective_date TEXT,
  sponsored_at TEXT NOT NULL,
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_sponsors_public
  ON sponsors (privacy_level, status, monthly_price_in_cents DESC, sponsored_at);

CREATE INDEX IF NOT EXISTS idx_sponsors_node
  ON sponsors (sponsor_node_id);
