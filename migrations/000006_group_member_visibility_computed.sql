-- +goose Up

-- Group member visibility is computed per request from the member repository
-- (ADR 0029, "Group invariants"); nothing derived is persisted. The stored
-- per-member copy of the anonymous flag on the legacy group member tables could
-- drift from the repository it described, so it is dropped. The group-level
-- anonymous flag of a legacy group stays: it is the group's own setting.

ALTER TABLE oci_group_members DROP COLUMN anonymous;
ALTER TABLE maven_group_members DROP COLUMN anonymous;
ALTER TABLE raw_group_members DROP COLUMN anonymous;
ALTER TABLE conan_group_members DROP COLUMN anonymous;
