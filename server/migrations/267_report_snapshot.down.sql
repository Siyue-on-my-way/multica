-- SIY-83: reverse the snapshot separation. report_history.data_snapshot is
-- left as-is (it still carries the v2 manifest or, for legacy rows, the
-- original inline payload); only the compressed-evidence table is dropped.
DROP TABLE IF EXISTS report_snapshot;
