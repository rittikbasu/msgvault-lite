-- msgvault-lite Gmail archive schema

-- ============================================================================
-- SOURCES & IDENTITY
-- ============================================================================

-- Message sources (Gmail accounts)
CREATE TABLE IF NOT EXISTS sources (
    id INTEGER PRIMARY KEY,
    source_type TEXT NOT NULL CHECK (source_type = 'gmail'),
    identifier TEXT NOT NULL,   -- Gmail account email
    display_name TEXT,

    google_user_id TEXT UNIQUE,

    -- Sync state
    last_sync_at DATETIME,
    sync_cursor TEXT,           -- Gmail historyId
    oauth_app TEXT,             -- named OAuth app binding (NULL = default)

    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,

    UNIQUE(source_type, identifier)
);

-- Participants (Gmail message addresses)
CREATE TABLE IF NOT EXISTS participants (
    id INTEGER PRIMARY KEY,
    email_address TEXT,
    display_name TEXT,
    domain TEXT,                -- extracted from email for aggregation

    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);


-- ============================================================================
-- CONVERSATIONS & MESSAGES
-- ============================================================================

-- Conversations (Gmail threads)
CREATE TABLE IF NOT EXISTS conversations (
    id INTEGER PRIMARY KEY,
    source_id INTEGER NOT NULL REFERENCES sources(id) ON DELETE CASCADE,

    -- Platform-specific ID for dedup on re-import
    source_conversation_id TEXT,

    title TEXT,                       -- email subject

    -- Denormalized stats (updated on message insert)
    message_count INTEGER DEFAULT 0,

    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,

    UNIQUE(source_id, source_conversation_id)
);


-- Messages (Gmail messages)
CREATE TABLE IF NOT EXISTS messages (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    conversation_id INTEGER NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    source_id INTEGER NOT NULL REFERENCES sources(id) ON DELETE CASCADE,

    -- Platform-specific ID for dedup
    source_message_id TEXT,

    -- Timestamps
    sent_at DATETIME,
    internal_date DATETIME,      -- Gmail internal date

    -- Sender
    sender_id INTEGER REFERENCES participants(id),

    -- Content
    subject TEXT,
    snippet TEXT,               -- preview/excerpt

    -- Size and attachment tracking
    size_estimate INTEGER,
    has_attachments BOOLEAN DEFAULT FALSE,
    attachment_count INTEGER DEFAULT 0,

    -- Remote deletion tombstone
    deleted_from_source_at DATETIME,

    -- Archival info
    archived_at DATETIME DEFAULT CURRENT_TIMESTAMP,

    UNIQUE(source_id, source_message_id)
);

-- Message recipients (From/To/Cc/Bcc for email)
CREATE TABLE IF NOT EXISTS message_recipients (
    id INTEGER PRIMARY KEY,
    message_id INTEGER NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    participant_id INTEGER NOT NULL REFERENCES participants(id) ON DELETE CASCADE,

    recipient_type TEXT NOT NULL CHECK (recipient_type IN ('from', 'to', 'cc', 'bcc')),
    display_name TEXT,             -- as it appeared in the message

    UNIQUE(message_id, participant_id, recipient_type)
);


-- ============================================================================
-- ATTACHMENTS & MEDIA
-- ============================================================================

-- Attachments (content-addressed storage)
CREATE TABLE IF NOT EXISTS attachments (
    id INTEGER PRIMARY KEY,
    message_id INTEGER NOT NULL REFERENCES messages(id) ON DELETE CASCADE,

    -- File identification
    filename TEXT,
    mime_type TEXT,
    size INTEGER,

    -- Content-addressed storage (deduplication)
    content_hash TEXT,              -- SHA-256 of content
    storage_path TEXT NOT NULL,     -- relative path: ab/abcd1234...

    source_attachment_id TEXT,      -- Gmail attachment ID

    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- ============================================================================
-- LABELS & ORGANIZATION
-- ============================================================================

-- Labels (Gmail labels, user tags)
CREATE TABLE IF NOT EXISTS labels (
    id INTEGER PRIMARY KEY,
    source_id INTEGER REFERENCES sources(id) ON DELETE CASCADE,  -- NULL for user-created

    source_label_id TEXT,           -- Gmail label ID
    name TEXT NOT NULL,
    label_type TEXT,                -- 'system', 'user', 'auto'

    UNIQUE(source_id, name)
);

-- Message labels
CREATE TABLE IF NOT EXISTS message_labels (
    message_id INTEGER NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    label_id INTEGER NOT NULL REFERENCES labels(id) ON DELETE CASCADE,

    PRIMARY KEY (message_id, label_id)
);

-- ============================================================================
-- RAW DATA STORAGE
-- ============================================================================

-- Message bodies (separated from messages to keep messages B-tree small)
CREATE TABLE IF NOT EXISTS message_bodies (
    message_id INTEGER PRIMARY KEY REFERENCES messages(id) ON DELETE CASCADE,
    body_text TEXT,
    body_html TEXT
);

-- Original message data (for re-parsing/export)
CREATE TABLE IF NOT EXISTS message_raw (
    message_id INTEGER PRIMARY KEY REFERENCES messages(id) ON DELETE CASCADE,

    raw_data BLOB NOT NULL,
    raw_format TEXT NOT NULL,       -- 'mime'

    compression TEXT DEFAULT 'zlib'
);

-- ============================================================================
-- SYNC STATE
-- ============================================================================

-- Sync runs (for debugging and resumability)
CREATE TABLE IF NOT EXISTS sync_runs (
    id INTEGER PRIMARY KEY,
    source_id INTEGER NOT NULL REFERENCES sources(id) ON DELETE CASCADE,

    started_at DATETIME NOT NULL,
    completed_at DATETIME,
    status TEXT DEFAULT 'running',  -- 'running', 'completed', 'failed', 'cancelled'

    messages_processed INTEGER DEFAULT 0,
    messages_added INTEGER DEFAULT 0,
    messages_updated INTEGER DEFAULT 0,
    errors_count INTEGER DEFAULT 0,

    error_message TEXT,
    cursor_before TEXT,
    cursor_after TEXT
);

-- Per-item sync outcomes, for diagnosing partial sync completion.
-- status='error' is actionable and contributes to sync_runs.errors_count.
-- status='skipped' records expected item churn, such as Gmail messages that
-- were deleted between a history/list response and raw-message fetch.
CREATE TABLE IF NOT EXISTS sync_run_items (
    id INTEGER PRIMARY KEY,
    sync_run_id INTEGER NOT NULL REFERENCES sync_runs(id) ON DELETE CASCADE,
    source_message_id TEXT NOT NULL,
    phase TEXT NOT NULL,
    status TEXT NOT NULL,
    error_kind TEXT NOT NULL,
    error_message TEXT NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- Sync checkpoints (for resumable imports)
CREATE TABLE IF NOT EXISTS sync_checkpoints (
    source_id INTEGER NOT NULL REFERENCES sources(id) ON DELETE CASCADE,
    checkpoint_type TEXT NOT NULL,  -- 'message_id', 'timestamp', 'page_token'
    checkpoint_value TEXT NOT NULL,

    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,

    PRIMARY KEY (source_id, checkpoint_type)
);


-- ============================================================================
-- INDEXES
-- ============================================================================

-- Sources
CREATE INDEX IF NOT EXISTS idx_sources_type ON sources(source_type);

-- Participants
CREATE UNIQUE INDEX IF NOT EXISTS idx_participants_email ON participants(email_address)
    WHERE email_address IS NOT NULL;


-- Conversations
CREATE INDEX IF NOT EXISTS idx_conversations_source ON conversations(source_id);

-- Messages
CREATE INDEX IF NOT EXISTS idx_messages_conversation ON messages(conversation_id, sent_at DESC);
CREATE INDEX IF NOT EXISTS idx_messages_source ON messages(source_id);
CREATE INDEX IF NOT EXISTS idx_messages_sender ON messages(sender_id);
CREATE INDEX IF NOT EXISTS idx_messages_sent_at ON messages(sent_at DESC);
CREATE INDEX IF NOT EXISTS idx_messages_deleted ON messages(source_id, deleted_from_source_at);
CREATE INDEX IF NOT EXISTS idx_messages_source_message_id ON messages(source_message_id);

-- Message IDs are durable archive cursors. Physical deletion could let SQLite
-- reuse the current maximum ROWID, so remote deletions are represented by
-- deleted_from_source_at tombstones instead.
CREATE TRIGGER IF NOT EXISTS messages_reject_delete
BEFORE DELETE ON messages
BEGIN
    SELECT RAISE(ABORT, 'message rows are insert-only');
END;

-- Message recipients
CREATE INDEX IF NOT EXISTS idx_message_recipients_message ON message_recipients(message_id);
CREATE INDEX IF NOT EXISTS idx_message_recipients_participant ON message_recipients(participant_id, recipient_type);


-- Attachments
CREATE INDEX IF NOT EXISTS idx_attachments_message ON attachments(message_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_attachments_msg_content_hash
    ON attachments(message_id, content_hash)
    WHERE content_hash IS NOT NULL AND content_hash != '';
CREATE INDEX IF NOT EXISTS idx_attachments_hash ON attachments(content_hash);
CREATE INDEX IF NOT EXISTS idx_attachments_storage_path ON attachments(storage_path);

-- Labels
CREATE INDEX IF NOT EXISTS idx_labels_source ON labels(source_id);
CREATE INDEX IF NOT EXISTS idx_message_labels_label ON message_labels(label_id);

-- Sync
CREATE INDEX IF NOT EXISTS idx_sync_runs_source ON sync_runs(source_id, started_at DESC);
CREATE INDEX IF NOT EXISTS idx_sync_run_items_run_status
    ON sync_run_items(sync_run_id, status, created_at DESC);

PRAGMA user_version = 1;
