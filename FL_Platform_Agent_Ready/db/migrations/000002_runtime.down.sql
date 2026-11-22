ALTER TABLE metric_points DROP CONSTRAINT metric_points_metric_value_check;
DROP INDEX metrics_global_once;
DROP TABLE model_presets, evaluations, rpc_receipts;
ALTER TABLE updates DROP COLUMN request_hash;
DROP INDEX artifact_upload_receipt;
ALTER TABLE artifacts DROP COLUMN upload_id, DROP COLUMN uploaded_by_task;
ALTER TABLE tasks DROP CONSTRAINT tasks_round_id_client_id_kind_attempt_key;
ALTER TABLE tasks DROP COLUMN lease_id, DROP COLUMN kind;
ALTER TABLE tasks ADD UNIQUE(round_id,client_id,attempt);
ALTER TABLE rounds DROP COLUMN aggregation_epoch, DROP COLUMN aggregation_owner,
 DROP COLUMN aggregation_until, DROP COLUMN frozen_update_ids, DROP COLUMN failure_code;
ALTER TABLE rounds DROP CONSTRAINT rounds_status_check;
ALTER TABLE rounds ADD CHECK (status IN
 ('planned','dispatching','collecting','aggregating','evaluating','completed','failed'));
ALTER TABLE clients DROP COLUMN shard_index;
DROP TABLE http_receipts, sessions;
ALTER TABLE users DROP COLUMN password_hash;
