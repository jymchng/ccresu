-- +migrate Up
-- PRD 6 R1: a session has two directories, not one.
--
-- project_path is where the session started, and runs_in_path is where it
-- actually runs. For a background session those differ by construction: it
-- "moves into its own worktree before editing files", under
-- <repo>/.claude/worktrees/<name>. Recording both is what lets a recovery
-- re-enter the tree the session occupies while the launch directory survives as
-- the documented fallback when a worktree is cleared.
--
-- The default is the empty string, not project_path, because "not observed yet"
-- and "the same as the project" are different facts: the first is filled in by
-- the next observation, the second is a session that never moved.
ALTER TABLE sessions ADD COLUMN runs_in_path TEXT NOT NULL DEFAULT '';

-- +migrate Down
-- The two facts collapse back into one, keeping the launch directory because
-- that is the value project_path has always held.
ALTER TABLE sessions DROP COLUMN runs_in_path;
