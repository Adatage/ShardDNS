CREATE TABLE IF NOT EXISTS zones (
    name        text PRIMARY KEY,
    primary_ns  text        NOT NULL,
    admin_email text        NOT NULL,
    serial      bigint      NOT NULL,
    refresh     integer     NOT NULL,
    retry       integer     NOT NULL,
    expire      integer     NOT NULL,
    minimum_ttl integer     NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS records (
    id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    zone       text        NOT NULL REFERENCES zones (name) ON DELETE CASCADE,
    name       text        NOT NULL,
    name_rev   text COLLATE "C" NOT NULL,
    type       text        NOT NULL,
    ttl        integer     NOT NULL,
    rdata      text        NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS records_rrset_member ON records (zone, name, type, md5(rdata));
CREATE INDEX IF NOT EXISTS records_name_rev ON records (zone, name_rev);
