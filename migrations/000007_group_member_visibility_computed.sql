-- +goose Up

-- Group member visibility is computed per request from the member repository
-- (ADR 0029, "Group invariants"); nothing derived is persisted. The stored
-- per-member copy of the anonymous flag on the legacy group member tables could
-- drift from the repository it described, so it is dropped. The group-level
-- anonymous flag of a legacy group stays: it is the group's own setting.
--
-- A legacy group used to expose to anonymous callers only the members flagged
-- anonymous. With the flag gone every member of an anonymous legacy group would
-- resolve, which would widen access, so a legacy group becomes anonymous only
-- when it was anonymous and every one of its members was: a mixed group turns
-- private and an operator re-enables it deliberately.

UPDATE oci_groups SET anonymous = false
WHERE anonymous AND EXISTS (SELECT 1 FROM oci_group_members m WHERE m.group_name = oci_groups.name AND NOT m.anonymous);
UPDATE maven_groups SET anonymous = false
WHERE anonymous AND EXISTS (SELECT 1 FROM maven_group_members m WHERE m.group_name = maven_groups.name AND NOT m.anonymous);
UPDATE raw_groups SET anonymous = false
WHERE anonymous AND EXISTS (SELECT 1 FROM raw_group_members m WHERE m.group_name = raw_groups.name AND NOT m.anonymous);
UPDATE conan_groups SET anonymous = false
WHERE anonymous AND EXISTS (SELECT 1 FROM conan_group_members m WHERE m.group_name = conan_groups.name AND NOT m.anonymous);

ALTER TABLE oci_group_members DROP COLUMN anonymous;
ALTER TABLE maven_group_members DROP COLUMN anonymous;
ALTER TABLE raw_group_members DROP COLUMN anonymous;
ALTER TABLE conan_group_members DROP COLUMN anonymous;
