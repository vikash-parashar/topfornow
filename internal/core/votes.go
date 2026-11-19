package core

import (
	"context"
	"errors"
	"time"
)

// ErrVoteLimit means too many different voters from one network.
var ErrVoteLimit = errors.New("too many votes from your network on this post")

// MaxVotersPerIP caps distinct voter cookies per hashed IP per post per day.
const MaxVotersPerIP = 5

// Vote records or changes a free vote. value is 1, -1, or 0 to clear.
// Votes belong to a post forever and never affect paid position.
func (s *Service) Vote(ctx context.Context, postID int64, voter, ipHash string, value int) (up, down int, err error) {
	if value == 0 {
		_, err = s.DB.Exec(ctx, `DELETE FROM votes WHERE post_id = $1 AND voter = $2`, postID, voter)
	} else {
		var exists bool
		s.DB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM votes WHERE post_id = $1 AND voter = $2)`, postID, voter).Scan(&exists)
		if !exists {
			var n int
			if err = s.DB.QueryRow(ctx, `SELECT count(*) FROM votes WHERE post_id = $1 AND ip_hash = $2 AND created_at > $3`,
				postID, ipHash, s.Now().Add(-24*time.Hour)).Scan(&n); err != nil {
				return
			}
			if n >= MaxVotersPerIP {
				err = ErrVoteLimit
				return
			}
		}
		_, err = s.DB.Exec(ctx, `INSERT INTO votes (post_id, voter, value, ip_hash, is_test) VALUES ($1,$2,$3,$4,$5)
			ON CONFLICT (post_id, voter) DO UPDATE SET value = EXCLUDED.value, updated_at = now()`, postID, voter, value, ipHash, s.Cfg.IsTest)
	}
	if err != nil {
		return
	}
	err = s.DB.QueryRow(ctx, `SELECT count(*) FILTER (WHERE value = 1), count(*) FILTER (WHERE value = -1) FROM votes WHERE post_id = $1`, postID).Scan(&up, &down)
	return
}

// MyVotes returns this visitor's votes keyed by post id.
func (s *Service) MyVotes(ctx context.Context, voter string, postIDs []int64) (map[int64]int, error) {
	out := map[int64]int{}
	if voter == "" || len(postIDs) == 0 {
		return out, nil
	}
	rows, err := s.DB.Query(ctx, `SELECT post_id, value FROM votes WHERE voter = $1 AND post_id = ANY($2)`, voter, postIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var v int
		if err := rows.Scan(&id, &v); err != nil {
			return nil, err
		}
		out[id] = v
	}
	return out, rows.Err()
}

// Report flags a post for the owner.
func (s *Service) Report(ctx context.Context, postID int64, reason, visitor string) error {
	_, err := s.DB.Exec(ctx, `INSERT INTO reports (post_id, reason, visitor, is_test) VALUES ($1,$2,$3,$4)`, postID, reason, visitor, s.Cfg.IsTest)
	return err
}
