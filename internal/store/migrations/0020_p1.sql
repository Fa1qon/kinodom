-- P1: watchlist, device playback capabilities and local family profiles.
CREATE TABLE watchlist (
    device TEXT NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('film', 'series')),
    release_id INTEGER NOT NULL REFERENCES releases(id) ON DELETE CASCADE,
    created_at INTEGER NOT NULL,
    PRIMARY KEY (device, kind, release_id)
);
CREATE INDEX watchlist_device_created ON watchlist(device, created_at DESC);

CREATE TABLE capability_profiles (
    device TEXT PRIMARY KEY,
    codecs TEXT NOT NULL DEFAULT '[]',
    audio TEXT NOT NULL DEFAULT '[]',
    max_width INTEGER NOT NULL DEFAULT 0,
    max_height INTEGER NOT NULL DEFAULT 0,
    subtitles TEXT NOT NULL DEFAULT '[]',
    preferred_mode TEXT NOT NULL DEFAULT 'auto',
    updated_at INTEGER NOT NULL
);

CREATE TABLE family_profiles (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL UNIQUE,
    pin_hash TEXT NOT NULL,
    allow_adult INTEGER NOT NULL DEFAULT 0,
    can_delete INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL
);
CREATE TABLE family_devices (
    device TEXT PRIMARY KEY,
    profile_id INTEGER NOT NULL REFERENCES family_profiles(id) ON DELETE CASCADE,
    updated_at INTEGER NOT NULL
);
