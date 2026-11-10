CREATE TABLE schema_migrations (
  version INTEGER PRIMARY KEY,
  applied_at TEXT NOT NULL
);

CREATE TABLE settings (
  id INTEGER PRIMARY KEY CHECK (id = 1),
  secret_fp TEXT NOT NULL DEFAULT '',
  bot_token_nonce BLOB,
  bot_token_ct BLOB,
  gateway_session_id TEXT NOT NULL DEFAULT '',
  gateway_sn INTEGER NOT NULL DEFAULT 0,
  updated_at TEXT NOT NULL
);

CREATE TABLE channel_binding (
  id INTEGER PRIMARY KEY CHECK (id = 1),
  guild_id TEXT NOT NULL DEFAULT '',
  guild_name TEXT NOT NULL DEFAULT '',
  voice_channel_id TEXT NOT NULL DEFAULT '',
  voice_channel_name TEXT NOT NULL DEFAULT '',
  voice_password_nonce BLOB,
  voice_password_ct BLOB,
  text_channel_id TEXT NOT NULL DEFAULT '',
  text_channel_name TEXT NOT NULL DEFAULT '',
  mirror_enabled INTEGER NOT NULL DEFAULT 0,
  mirror_format TEXT NOT NULL DEFAULT 'kmarkdown',
  updated_at TEXT NOT NULL
);

CREATE TABLE tts_settings (
  id INTEGER PRIMARY KEY CHECK (id = 1),
  provider TEXT NOT NULL DEFAULT 'edge',
  edge_voice TEXT NOT NULL DEFAULT 'zh-CN-XiaoxiaoNeural',
  rate REAL NOT NULL DEFAULT 1.0,
  volume REAL NOT NULL DEFAULT 0.8,
  api_url TEXT NOT NULL DEFAULT '',
  api_model TEXT NOT NULL DEFAULT '',
  api_voice TEXT NOT NULL DEFAULT '',
  api_key_nonce BLOB,
  api_key_ct BLOB,
  updated_at TEXT NOT NULL
);

CREATE TABLE voice_presets (
  id INTEGER PRIMARY KEY,
  provider TEXT NOT NULL,
  alias TEXT NOT NULL,
  voice_id TEXT NOT NULL,
  sort_order INTEGER NOT NULL DEFAULT 0,
  UNIQUE (provider, alias)
);

CREATE TABLE speak_history (
  id INTEGER PRIMARY KEY,
  created_at TEXT NOT NULL,
  text TEXT NOT NULL,
  provider TEXT NOT NULL,
  voice_id TEXT NOT NULL,
  rate REAL NOT NULL,
  volume REAL NOT NULL,
  voice_channel_id TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL,
  error TEXT NOT NULL DEFAULT '',
  synth_ms INTEGER,
  play_ms INTEGER
);

CREATE INDEX idx_speak_history_created ON speak_history (created_at DESC);

CREATE TABLE module_state (
  module TEXT PRIMARY KEY,
  enabled INTEGER NOT NULL DEFAULT 0,
  config_json TEXT NOT NULL DEFAULT '{}'
);

INSERT INTO settings (id, updated_at) VALUES (1, '1970-01-01T00:00:00Z');
INSERT INTO channel_binding (id, updated_at) VALUES (1, '1970-01-01T00:00:00Z');
INSERT INTO tts_settings (id, updated_at) VALUES (1, '1970-01-01T00:00:00Z');

INSERT INTO voice_presets (provider, alias, voice_id, sort_order) VALUES
  ('edge', '晓晓', 'zh-CN-XiaoxiaoNeural', 1),
  ('edge', '晓伊', 'zh-CN-XiaoyiNeural', 2),
  ('edge', '云希', 'zh-CN-YunxiNeural', 3),
  ('edge', '云扬', 'zh-CN-YunyangNeural', 4),
  ('edge', '云健', 'zh-CN-YunjianNeural', 5),
  ('edge', '云夏', 'zh-CN-YunxiaNeural', 6),
  ('edge', '晓北', 'zh-CN-liaoning-XiaobeiNeural', 7),
  ('edge', '晓妮', 'zh-CN-shaanxi-XiaoniNeural', 8),
  ('edge', '晓臻', 'zh-TW-HsiaoChenNeural', 9),
  ('edge', '晓雨', 'zh-TW-HsiaoYuNeural', 10),
  ('edge', '云哲', 'zh-TW-YunJheNeural', 11),
  ('edge', '晓佳', 'zh-HK-HiuGaaiNeural', 12),
  ('edge', '晓曼', 'zh-HK-HiuMaanNeural', 13),
  ('edge', '云龙', 'zh-HK-WanLungNeural', 14);

INSERT INTO module_state (module, enabled, config_json) VALUES ('speak', 1, '{}');
