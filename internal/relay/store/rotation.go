package store

import "context"

type RotationInput struct {
	RotationID   string
	ControllerID string
	OldKeyID     string
	NewKeyID     string
	SessionID    string
	NewPublicKey []byte
}

func (s *Store) ExpireRotations(ctx context.Context) (int64, error) {
	tag, err := s.pool.Exec(ctx, `UPDATE relay_controller_keys SET state='revoked',rotation_nonce=NULL,rotation_expires_at=NULL,revoked_at=$1 WHERE state='pending' AND rotation_expires_at<=$1`, s.now().UTC())
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
