-- +migrate Up
-- PRD 3 (R8): no lifecycle decision depends on a PID.
--
-- `ppid` and `process_start_time` were added so a recycled PID could never be
-- mistaken for the original process, but nothing ever wrote them: discovery sets
-- `pid` from the published listing and leaves the other two at zero, they are
-- scanned back as zero, and the package that would compute them
-- (`internal/process`) has no production caller. A column that promises a check
-- nothing performs is worse than no column, because the next reader believes it.
--
-- `pid` stays. It is what the supervisor's listing reported, `ccresu inspect`
-- shows it, and it is useful evidence even though R8 removes it from every
-- decision: liveness comes from the published state (PRD 2) and from
-- `claude daemon status` for the supervisor itself (PRD 3 R1).
ALTER TABLE sessions DROP COLUMN ppid;

ALTER TABLE sessions DROP COLUMN process_start_time;

-- +migrate Down
-- SQLite appends a re-added column at the end of the table rather than restoring
-- its original position. Nothing depends on physical order: `sessionColumns` in
-- sessions.go names every column explicitly, so a rolled-back database reads
-- back the same rows through the same list.
ALTER TABLE sessions ADD COLUMN ppid INTEGER NOT NULL DEFAULT 0;

ALTER TABLE sessions ADD COLUMN process_start_time INTEGER NOT NULL DEFAULT 0;
