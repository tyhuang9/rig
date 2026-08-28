CREATE INDEX relay_outbound_pending_replay ON relay_outbound_commands(controller_id,sent_at,message_id) WHERE state='prepared';
