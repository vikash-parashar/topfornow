-- TopForNow initial schema.
-- Money is always stored as integer US cents.

CREATE TABLE posts (
    id              BIGSERIAL PRIMARY KEY,
    slug            TEXT NOT NULL UNIQUE,
    kind            TEXT NOT NULL CHECK (kind IN ('opinion', 'creator', 'product')),
    headline        TEXT NOT NULL,
    body            TEXT NOT NULL DEFAULT '',
    link_url        TEXT,
    link_host       TEXT,
    embed_provider  TEXT CHECK (embed_provider IN ('youtube', 'instagram')),
    embed_ref       TEXT,
    author_name     TEXT NOT NULL,
    contact_email   TEXT,
    reply_to_id     BIGINT REFERENCES posts(id),
    status          TEXT NOT NULL CHECK (status IN ('needs_review', 'approved', 'rejected', 'published', 'removed')),
    review_notes    TEXT NOT NULL DEFAULT '',
    round           INT,
    price_cents     BIGINT,
    published_at    TIMESTAMPTZ,
    dethroned_at    TIMESTAMPTZ,
    removed_at      TIMESTAMPTZ,
    removed_reason  TEXT,
    edit_token      TEXT NOT NULL UNIQUE,
    visitor         TEXT,
    is_test         BOOLEAN NOT NULL DEFAULT false,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- The core invariant: every spotlight round is published exactly once.
CREATE UNIQUE INDEX posts_one_per_round ON posts (round) WHERE round IS NOT NULL;
CREATE INDEX posts_status ON posts (status);

-- Single-row spotlight state. rounds_published counts every published post,
-- including the free founding post (round 0). The next round number equals
-- rounds_published, and the price for round r (r >= 1) is 100 << (r-1) cents.
CREATE TABLE spotlight (
    id                INT PRIMARY KEY CHECK (id = 1),
    rounds_published  INT NOT NULL DEFAULT 0,
    current_post_id   BIGINT REFERENCES posts(id),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);
INSERT INTO spotlight (id) VALUES (1);

-- A reservation (hold) gives one buyer an exclusive window to pay the quoted
-- price for one specific round.
CREATE TABLE reservations (
    id                   UUID PRIMARY KEY,
    post_id              BIGINT NOT NULL REFERENCES posts(id),
    round                INT NOT NULL,
    price_cents          BIGINT NOT NULL CHECK (price_cents > 0),
    currency             TEXT NOT NULL DEFAULT 'USD',
    status               TEXT NOT NULL CHECK (status IN ('active', 'paid', 'expired', 'cancelled', 'lost')),
    provider             TEXT NOT NULL,
    provider_session_id  TEXT,
    checkout_url         TEXT,
    visitor              TEXT,
    expires_at           TIMESTAMPTZ NOT NULL,
    is_test              BOOLEAN NOT NULL DEFAULT false,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- At most one active hold in the whole system.
CREATE UNIQUE INDEX reservations_one_active ON reservations ((true)) WHERE status = 'active';
CREATE INDEX reservations_session ON reservations (provider_session_id);

CREATE TABLE payments (
    id                   BIGSERIAL PRIMARY KEY,
    provider             TEXT NOT NULL,
    provider_payment_id  TEXT NOT NULL,
    reservation_id       UUID REFERENCES reservations(id),
    post_id              BIGINT REFERENCES posts(id),
    amount_cents         BIGINT NOT NULL,
    currency             TEXT NOT NULL,
    status               TEXT NOT NULL CHECK (status IN ('processing', 'succeeded', 'failed', 'cancelled', 'refund_pending', 'refunded', 'refund_failed')),
    outcome              TEXT NOT NULL DEFAULT '',
    refunded_cents       BIGINT NOT NULL DEFAULT 0,
    provider_refund_id   TEXT,
    refund_requested_at  TIMESTAMPTZ,
    refund_attempts      INT NOT NULL DEFAULT 0,
    dispute_status       TEXT,
    is_test              BOOLEAN NOT NULL DEFAULT false,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (provider, provider_payment_id)
);
CREATE INDEX payments_status ON payments (status);

CREATE TABLE webhook_events (
    id           BIGSERIAL PRIMARY KEY,
    provider     TEXT NOT NULL,
    event_id     TEXT NOT NULL,
    event_type   TEXT NOT NULL,
    payload      JSONB NOT NULL,
    result       TEXT NOT NULL DEFAULT '',
    is_test      BOOLEAN NOT NULL DEFAULT false,
    received_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (provider, event_id)
);

CREATE TABLE votes (
    post_id     BIGINT NOT NULL REFERENCES posts(id) ON DELETE CASCADE,
    voter       TEXT NOT NULL,
    value       SMALLINT NOT NULL CHECK (value IN (-1, 1)),
    ip_hash     TEXT NOT NULL,
    is_test     BOOLEAN NOT NULL DEFAULT false,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (post_id, voter)
);
CREATE INDEX votes_ip ON votes (post_id, ip_hash);

CREATE TABLE events (
    id         BIGSERIAL PRIMARY KEY,
    ts         TIMESTAMPTZ NOT NULL DEFAULT now(),
    type       TEXT NOT NULL,
    visitor    TEXT NOT NULL DEFAULT '',
    post_id    BIGINT REFERENCES posts(id) ON DELETE CASCADE,
    source     TEXT NOT NULL DEFAULT '',
    path       TEXT NOT NULL DEFAULT '',
    is_bot     BOOLEAN NOT NULL DEFAULT false,
    is_test    BOOLEAN NOT NULL DEFAULT false
);
CREATE INDEX events_post_type ON events (post_id, type);
CREATE INDEX events_type_ts ON events (type, ts);
CREATE INDEX events_visitor ON events (visitor);

CREATE TABLE reports (
    id          BIGSERIAL PRIMARY KEY,
    post_id     BIGINT NOT NULL REFERENCES posts(id) ON DELETE CASCADE,
    reason      TEXT NOT NULL,
    visitor     TEXT NOT NULL DEFAULT '',
    status      TEXT NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'dismissed', 'actioned')),
    is_test     BOOLEAN NOT NULL DEFAULT false,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Only used by the built-in sandbox checkout simulator.
CREATE TABLE sim_sessions (
    id              TEXT PRIMARY KEY,
    amount_cents    BIGINT NOT NULL,
    metadata        JSONB NOT NULL,
    status          TEXT NOT NULL DEFAULT 'open',
    payment_id      TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
