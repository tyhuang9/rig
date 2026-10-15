-- Materialized local and GitHub releases use 32 lowercase hexadecimal bytes.
-- Replace only the retained-head release identity check. Exact parent, claim,
-- ordering and timestamp checks remain, and no existing history is rewritten.
-- Byte lengths reject embedded NUL suffixes in both supported identity formats.
DROP TRIGGER lan_gateway_rebind_runtime_head_exact_insert;

CREATE TRIGGER lan_gateway_rebind_runtime_head_exact_insert
BEFORE INSERT ON lan_gateway_rebind_runtime_heads
WHEN NOT (
    length(NEW.app_id)=36 AND substr(NEW.app_id,9,1)='-'
      AND substr(NEW.app_id,14,1)='-' AND substr(NEW.app_id,19,1)='-'
      AND substr(NEW.app_id,24,1)='-'
      AND substr(NEW.app_id,1,8) NOT GLOB '*[^0-9a-f]*'
      AND substr(NEW.app_id,10,4) NOT GLOB '*[^0-9a-f]*'
      AND substr(NEW.app_id,15,4) NOT GLOB '*[^0-9a-f]*'
      AND substr(NEW.app_id,20,4) NOT GLOB '*[^0-9a-f]*'
      AND substr(NEW.app_id,25,12) NOT GLOB '*[^0-9a-f]*'
    AND length(NEW.deployment_id)=36 AND substr(NEW.deployment_id,9,1)='-'
      AND substr(NEW.deployment_id,14,1)='-' AND substr(NEW.deployment_id,19,1)='-'
      AND substr(NEW.deployment_id,24,1)='-'
      AND substr(NEW.deployment_id,1,8) NOT GLOB '*[^0-9a-f]*'
      AND substr(NEW.deployment_id,10,4) NOT GLOB '*[^0-9a-f]*'
      AND substr(NEW.deployment_id,15,4) NOT GLOB '*[^0-9a-f]*'
      AND substr(NEW.deployment_id,20,4) NOT GLOB '*[^0-9a-f]*'
      AND substr(NEW.deployment_id,25,12) NOT GLOB '*[^0-9a-f]*'
    AND typeof(NEW.release_id)='text'
    AND (
      (length(CAST(NEW.release_id AS BLOB))=36 AND length(NEW.release_id)=36
        AND substr(NEW.release_id,9,1)='-'
        AND substr(NEW.release_id,14,1)='-' AND substr(NEW.release_id,19,1)='-'
        AND substr(NEW.release_id,24,1)='-'
        AND substr(NEW.release_id,1,8) NOT GLOB '*[^0-9a-f]*'
        AND substr(NEW.release_id,10,4) NOT GLOB '*[^0-9a-f]*'
        AND substr(NEW.release_id,15,4) NOT GLOB '*[^0-9a-f]*'
        AND substr(NEW.release_id,20,4) NOT GLOB '*[^0-9a-f]*'
        AND substr(NEW.release_id,25,12) NOT GLOB '*[^0-9a-f]*')
      OR (length(NEW.release_id)=32 AND length(CAST(NEW.release_id AS BLOB))=32
        AND NEW.release_id NOT GLOB '*[^0-9a-f]*')
    )
    AND length(NEW.updated_at)=30 AND substr(NEW.updated_at,20,1)='.'
    AND substr(NEW.updated_at,-1,1)='Z'
    AND substr(NEW.updated_at,21,9) NOT GLOB '*[^0-9]*'
    AND julianday(NEW.updated_at) IS NOT NULL
    AND EXISTS (
      SELECT 1
      FROM lan_gateway_rebind_claims c
      JOIN generated_runtime_active_heads h ON h.app_id=NEW.app_id
      JOIN applications a ON a.id=h.app_id
      JOIN generated_runtime_deployments d
        ON d.app_id=h.app_id AND d.deployment_id=h.deployment_id
       AND d.release_id=h.release_id AND d.candidate_slot=h.slot
      JOIN releases r ON r.app_id=h.app_id AND r.id=h.release_id
      WHERE c.operation_id=NEW.operation_id
        AND c.spec_format_version=2 AND c.runtime_heads_format_version=1
        AND c.state='prepared' AND c.state_sequence=1
        AND NEW.ordinal=(SELECT COUNT(*)+1 FROM lan_gateway_rebind_runtime_heads prior
                         WHERE prior.operation_id=NEW.operation_id)
        AND NEW.ordinal<=c.runtime_heads_count
        AND NEW.app_id>COALESCE((SELECT MAX(prior.app_id)
                                 FROM lan_gateway_rebind_runtime_heads prior
                                 WHERE prior.operation_id=NEW.operation_id),'')
        AND h.deployment_id=NEW.deployment_id AND h.release_id=NEW.release_id
        AND h.slot=NEW.slot AND h.generation=NEW.generation
        AND NEW.updated_at=(substr(h.updated_at,1,19)||'.'||
            CASE WHEN length(h.updated_at)=20 THEN '000000000'
                 ELSE substr(substr(h.updated_at,21,length(h.updated_at)-21)||'000000000',1,9)
            END||'Z')
        AND h.generation>0
        AND a.archived_at IS NULL
    )
)
BEGIN SELECT RAISE(ABORT, 'LAN gateway rebind runtime head is not exact'); END;
