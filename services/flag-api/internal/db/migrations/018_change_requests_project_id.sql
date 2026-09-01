-- Migration 018: project_id on change_requests (TEN-1a-3).
--
-- change_requests had no project_id at all, and flags.key is unique only per
-- (project_id, key), so GET /api/v1/change-requests (already reachable by
-- ANY authenticated user -- see the documented SEC-3 exemption in
-- cmd/authz_routes_test.go, which this migration does not change) leaked
-- every project's pending/approved/rejected requests, and
-- Approve/RejectChangeRequest matched by id alone with no project check.
--
-- Nullable, not NOT NULL: existing rows cannot be reliably attributed to a
-- project after the fact -- same reasoning as migrations 016 and 017.
ALTER TABLE change_requests ADD COLUMN IF NOT EXISTS project_id UUID REFERENCES projects(id);
CREATE INDEX IF NOT EXISTS idx_change_requests_project_id ON change_requests(project_id);
