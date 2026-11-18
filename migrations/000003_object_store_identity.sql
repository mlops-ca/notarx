-- +goose Up

-- Single-node identity of a filesystem object store (ADR 0017, "Filesystem
-- driver"). The gateway writes one row at first boot with the driver name and
-- the random root ID kept in <root>/.notarx-root; a gateway whose root ID does
-- not match refuses to start, which catches a second node, a restored volume
-- and a wrong mount. The singleton column and its CHECK keep the table to one row.
CREATE TABLE public.object_store_identity (
    singleton boolean DEFAULT true NOT NULL,
    driver text NOT NULL,
    root_id text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT object_store_identity_pkey PRIMARY KEY (singleton),
    CONSTRAINT object_store_identity_singleton_check CHECK (singleton),
    CONSTRAINT object_store_identity_driver_check CHECK (driver <> ''),
    CONSTRAINT object_store_identity_root_id_check CHECK (root_id <> '')
);
