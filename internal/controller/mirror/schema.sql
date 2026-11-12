-- Copyright 2026 Lingfei Kong <colin404@foxmail.com>
--
-- Licensed under the Apache License, Version 2.0 (the "License");
-- you may not use this file except in compliance with the License.
-- You may obtain a copy of the License at
--
--     http://www.apache.org/licenses/LICENSE-2.0
--
-- Unless required by applicable law or agreed to in writing, software
-- distributed under the License is distributed on an "AS IS" BASIS,
-- WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
-- See the License for the specific language governing permissions and
-- limitations under the License.
--
-- The read model for the declarative control plane. Nothing here is a source of
-- truth: every row is derived from an object served by oai-declserver, and the
-- whole schema can be dropped without losing information.
--
-- Not managed by GORM AutoMigrate. Partitioning, GIN indexes and jsonb_path_ops
-- are PostgreSQL features AutoMigrate cannot express, and the tables are
-- derived data, so a rebuild-from-scratch is an acceptable schema change.
--
-- Per-kind partitions are created from the Go registry (see EnsureSchema); they
-- cannot be listed here because the registry is the single source of the kind
-- list.

-- {{schema}} is substituted from the configured schema name; PostgreSQL has no
-- placeholder for an identifier, and the DDL must follow the configuration
-- rather than a literal.
CREATE SCHEMA IF NOT EXISTS {{schema}};

CREATE TABLE IF NOT EXISTS {{schema}}.object (
    group_name       text        NOT NULL,
    kind             text        NOT NULL,
    namespace        text        NOT NULL,
    name             text        NOT NULL,
    uid              text        NOT NULL,
    resource_version bigint      NOT NULL,
    generation       bigint      NOT NULL DEFAULT 0,
    -- The object's creation time. Stored because the row type a reader rebuilds
    -- carries it, and a projection that silently zeroed it would change what the
    -- API returns rather than only where it is read from.
    created_at       timestamptz NOT NULL DEFAULT 'epoch',
    -- Promoted from spec so the common filters are plain column predicates.
    enabled          boolean,
    display_name     text,
    owner            text,
    labels           jsonb       NOT NULL DEFAULT '{}'::jsonb,
    annotations      jsonb       NOT NULL DEFAULT '{}'::jsonb,
    spec             jsonb       NOT NULL DEFAULT '{}'::jsonb,
    status           jsonb       NOT NULL DEFAULT '{}'::jsonb,
    -- Two hashes on purpose: the event path compares spec_hash only, so a
    -- controller patching status does not turn the mirror into a hot write
    -- path. status_hash is refreshed by the periodic sweep.
    spec_hash        text        NOT NULL,
    status_hash      text        NOT NULL,
    -- When this row was last written. An audit column, not a deletion
    -- watermark: deciding which rows to remove is done in memory, because
    -- stamping every row on every pass would cost one UPDATE per object per
    -- sweep.
    observed_at      timestamptz NOT NULL,
    PRIMARY KEY (group_name, kind, namespace, name)
) PARTITION BY LIST (kind);

-- Catch-all so a kind that gains no partition yet still persists rows.
CREATE TABLE IF NOT EXISTS {{schema}}.object_default
    PARTITION OF {{schema}}.object DEFAULT;

CREATE INDEX IF NOT EXISTS object_sweep_idx
    ON {{schema}}.object (group_name, kind, namespace, observed_at);
CREATE INDEX IF NOT EXISTS object_labels_idx
    ON {{schema}}.object USING gin (labels);
CREATE INDEX IF NOT EXISTS object_spec_idx
    ON {{schema}}.object USING gin (spec jsonb_path_ops);

-- Projection watermark per kind: the read side decides from it whether the
-- mirror may serve, and monitoring derives lag from it.
CREATE TABLE IF NOT EXISTS {{schema}}.state (
    group_name    text        NOT NULL,
    kind          text        NOT NULL,
    object_count  bigint      NOT NULL DEFAULT 0,
    last_sweep_at timestamptz,
    last_rv       bigint      NOT NULL DEFAULT 0,
    updated_at    timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (group_name, kind)
);
