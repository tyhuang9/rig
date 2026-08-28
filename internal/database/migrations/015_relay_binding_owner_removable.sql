CREATE INDEX relay_binding_owner_removable
ON relay_installation_bindings(owner_user_id, updated_at DESC, binding_id DESC)
WHERE state IN ('authorized','access_lost','removal_pending');
