package tenantagent

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

type schemaQuery interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

// A bounded, read-only structural probe, not authentication. It checks required
// types/nullability, usable unique keys, exact FK columns/actions, named validated
// checks and the ownership trigger. Exact CHECK/function expressions and full
// catalog equivalence require the migration tests/rehearsal, not this probe.
func schemaReady(ctx context.Context, q schemaQuery) error {
	if q == nil {
		return unavailable()
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	for _, query := range []string{requiredColumns, requiredKeys, requiredForeignKeys, requiredChecks} {
		var ready bool
		if err := q.QueryRow(ctx, query).Scan(&ready); err != nil || !ready {
			return unavailable()
		}
	}
	return nil
}

const requiredColumns = `SELECT bool_and(coalesce(c.relkind='r'
 AND a.atttypid=to_regtype(e.typ) AND a.attnotnull=e.required AND NOT a.attisdropped,false))
 FROM (VALUES
 ('tenant_agents','id','uuid',true),('tenant_agents','organization_id','uuid',true),
 ('tenant_agents','registry_slot','smallint',true),('tenant_agents','name','text',true),
 ('tenant_agents','state','text',true),('tenant_agents','revision','integer',true),
 ('tenant_agents','created_by','uuid',true),('tenant_agents','created_at','timestamptz',true),
 ('tenant_agents','updated_at','timestamptz',true),
 ('agent_issue_budgets','organization_id','uuid',true),('agent_issue_budgets','window_started_at','timestamptz',true),
 ('agent_issue_budgets','used_units','smallint',true),('agent_issue_budgets','updated_at','timestamptz',true),
 ('agent_enrollments','agent_id','uuid',true),('agent_enrollments','organization_id','uuid',true),
 ('agent_enrollments','enrollment_id','uuid',true),('agent_enrollments','digest','bytea',true),
 ('agent_enrollments','issuer_user_id','uuid',true),('agent_enrollments','generation','integer',true),
 ('agent_enrollments','budget_window','timestamptz',true),('agent_enrollments','reserved_units','smallint',true),
 ('agent_enrollments','created_at','timestamptz',true),('agent_enrollments','expires_at','timestamptz',true),
 ('agent_enrollments','consumed_at','timestamptz',false),('agent_enrollments','revoked_at','timestamptz',false),
 ('agent_credentials','agent_id','uuid',true),('agent_credentials','organization_id','uuid',true),
 ('agent_credentials','credential_id','uuid',true),('agent_credentials','digest','bytea',true),
 ('agent_credentials','generation','integer',true),('agent_credentials','created_at','timestamptz',true),
 ('agent_credentials','expires_at','timestamptz',true),('agent_credentials','revoked_at','timestamptz',false),
 ('agent_events','id','uuid',true),('agent_events','organization_id','uuid',true),
 ('agent_events','agent_id','uuid',true),('agent_events','actor_kind','text',true),
 ('agent_events','actor_user_id','uuid',false),('agent_events','enrollment_id','uuid',false),
 ('agent_events','action','text',true),('agent_events','agent_revision','integer',true),
 ('agent_events','credential_generation','integer',false),('agent_events','before_state','text',false),
 ('agent_events','after_state','text',true),('agent_events','created_at','timestamptz',true)
 ) e(rel,col,typ,required)
 LEFT JOIN pg_class c ON c.relname=e.rel AND c.relnamespace='public'::regnamespace
 LEFT JOIN pg_attribute a ON a.attrelid=c.oid AND a.attname=e.col`

const requiredKeys = `SELECT bool_and(EXISTS(
 SELECT 1 FROM pg_constraint k JOIN pg_index i ON i.indexrelid=k.conindid
 WHERE k.conrelid=to_regclass('public.'||e.rel) AND k.contype::text=e.kind
 AND k.convalidated AND NOT k.condeferrable AND i.indisvalid AND i.indisready
 AND i.indpred IS NULL AND i.indexprs IS NULL
 AND (SELECT array_agg(a.attname::text ORDER BY ord) FROM unnest(k.conkey) WITH ORDINALITY x(num,ord)
 JOIN pg_attribute a ON a.attrelid=k.conrelid AND a.attnum=x.num)=e.cols))
 FROM (VALUES
 ('tenant_agents','p',ARRAY['id']),('tenant_agents','u',ARRAY['id','organization_id']),
 ('tenant_agents','u',ARRAY['organization_id','registry_slot']),
 ('agent_issue_budgets','p',ARRAY['organization_id']),
 ('agent_enrollments','p',ARRAY['agent_id']),('agent_enrollments','u',ARRAY['enrollment_id']),
 ('agent_enrollments','u',ARRAY['digest']),('agent_credentials','p',ARRAY['agent_id']),
 ('agent_credentials','u',ARRAY['credential_id']),('agent_credentials','u',ARRAY['digest']),
 ('agent_events','p',ARRAY['id'])
 ) e(rel,kind,cols)`

const requiredForeignKeys = `SELECT bool_and(EXISTS(
 SELECT 1 FROM pg_constraint k WHERE k.conrelid=to_regclass('public.'||e.rel)
 AND k.confrelid=to_regclass('public.'||e.target) AND k.contype='f' AND k.convalidated
 AND NOT k.condeferrable AND k.confdeltype='a' AND k.confupdtype='a' AND k.confmatchtype='s'
 AND (SELECT array_agg(a.attname::text ORDER BY ord) FROM unnest(k.conkey) WITH ORDINALITY x(num,ord)
 JOIN pg_attribute a ON a.attrelid=k.conrelid AND a.attnum=x.num)=e.cols
 AND (SELECT array_agg(a.attname::text ORDER BY ord) FROM unnest(k.confkey) WITH ORDINALITY x(num,ord)
 JOIN pg_attribute a ON a.attrelid=k.confrelid AND a.attnum=x.num)=e.targetcols))
 FROM (VALUES
 ('tenant_agents',ARRAY['organization_id'],'organizations',ARRAY['id']),
 ('tenant_agents',ARRAY['organization_id','created_by'],'organization_memberships',ARRAY['organization_id','user_id']),
 ('agent_issue_budgets',ARRAY['organization_id'],'organizations',ARRAY['id']),
 ('agent_enrollments',ARRAY['agent_id','organization_id'],'tenant_agents',ARRAY['id','organization_id']),
 ('agent_enrollments',ARRAY['organization_id','issuer_user_id'],'organization_memberships',ARRAY['organization_id','user_id']),
 ('agent_credentials',ARRAY['agent_id','organization_id'],'tenant_agents',ARRAY['id','organization_id']),
 ('agent_events',ARRAY['agent_id','organization_id'],'tenant_agents',ARRAY['id','organization_id']),
 ('agent_events',ARRAY['actor_user_id'],'users',ARRAY['id'])
 ) e(rel,cols,target,targetcols)`

const requiredChecks = `SELECT bool_and(EXISTS(
 SELECT 1 FROM pg_constraint k WHERE k.conrelid=to_regclass('public.'||e.rel)
 AND k.conname=e.name AND k.contype='c' AND k.convalidated))
 AND EXISTS(SELECT 1 FROM pg_trigger t JOIN pg_proc p ON p.oid=t.tgfoid
 WHERE t.tgrelid=to_regclass('public.tenant_agents') AND t.tgname='tenant_agent_immutable_owner'
 AND NOT t.tgisinternal AND t.tgenabled='O' AND t.tgtype=19
 AND t.tgqual IS NULL AND t.tgnargs=0 AND t.tgattr=''::int2vector
 AND p.oid=to_regprocedure('public.tenant_agent_immutable_owner()') AND p.prorettype='trigger'::regtype)
 FROM (VALUES
 ('tenant_agents','tenant_agents_slot'),('tenant_agents','tenant_agents_name'),
 ('tenant_agents','tenant_agents_state'),('tenant_agents','tenant_agents_revision'),
 ('tenant_agents','tenant_agents_times'),('agent_issue_budgets','agent_issue_budgets_units'),
 ('agent_issue_budgets','agent_issue_budgets_times'),('agent_enrollments','agent_enrollments_digest'),
 ('agent_enrollments','agent_enrollments_generation'),('agent_enrollments','agent_enrollments_reservation'),
 ('agent_enrollments','agent_enrollments_times'),('agent_credentials','agent_credentials_digest'),
 ('agent_credentials','agent_credentials_generation'),('agent_credentials','agent_credentials_times'),
 ('agent_events','agent_events_actor'),('agent_events','agent_events_action'),
 ('agent_events','agent_events_revision'),('agent_events','agent_events_states'),('agent_events','agent_events_time')
 ) e(rel,name)`
