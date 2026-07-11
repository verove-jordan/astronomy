package store

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"
)

// S3Object is one persisted local-rel → classified-S3-key mapping (see migration 0009 / internal/s3layout).
// It is the ledger that makes the non-reversible classified layout recoverable: the serving fallback, the
// full-S3 pull and the remove-local verification all resolve the real key from the file's local rel path.
type S3Object struct {
	ID        int64  `json:"id" db:"id"`
	Bucket    string `json:"bucket" db:"bucket"`
	Prefix    string `json:"prefix" db:"prefix"`
	LocalRel  string `json:"local_rel" db:"local_rel"`
	S3Key     string `json:"s3_key" db:"s3_key"`
	Size      int64  `json:"size" db:"size"`
	CreatedAt int64  `json:"created_at" db:"created_at"`
	UpdatedAt int64  `json:"updated_at" db:"updated_at"`
}

const s3ObjCols = `id,bucket,prefix,local_rel,s3_key,size,created_at,updated_at`

// UpsertS3Object records (or refreshes) the mapping for one uploaded file, keyed by (bucket, prefix,
// local_rel). Re-uploading the same file updates its key/size, so the ledger self-heals.
func (s *Store) UpsertS3Object(ctx context.Context, o S3Object) error {
	now := nowMs()
	_, err := s.pool.Exec(ctx,
		`INSERT INTO s3_objects(bucket,prefix,local_rel,s3_key,size,created_at,updated_at)
		 VALUES($1,$2,$3,$4,$5,$6,$6)
		 ON CONFLICT (bucket,prefix,local_rel)
		 DO UPDATE SET s3_key=EXCLUDED.s3_key, size=EXCLUDED.size, updated_at=EXCLUDED.updated_at`,
		o.Bucket, o.Prefix, o.LocalRel, o.S3Key, o.Size, now)
	return err
}

// GetS3Object returns the mapping for one local rel path, or ok=false when none exists (the caller then
// uses the legacy data/<rel> key).
func (s *Store) GetS3Object(ctx context.Context, bucket, prefix, localRel string) (S3Object, bool, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+s3ObjCols+` FROM s3_objects WHERE bucket=$1 AND prefix=$2 AND local_rel=$3`,
		bucket, prefix, localRel)
	if err != nil {
		return S3Object{}, false, err
	}
	defer rows.Close()
	o, err := pgx.CollectExactlyOneRow(rows, pgx.RowToStructByName[S3Object])
	if err == pgx.ErrNoRows {
		return S3Object{}, false, nil
	}
	return o, err == nil, err
}

// ListS3ObjectsUnder returns every mapping whose local_rel is relPrefix itself or a descendant of it —
// so a full-S3 pull reassembles a source folder's files (scattered across the classified roots) back to
// their original local tree.
func (s *Store) ListS3ObjectsUnder(ctx context.Context, bucket, prefix, relPrefix string) ([]S3Object, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+s3ObjCols+` FROM s3_objects
		 WHERE bucket=$1 AND prefix=$2 AND (local_rel=$3 OR local_rel LIKE $4)`,
		bucket, prefix, relPrefix, likeEscape(relPrefix)+"/%")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return pgx.CollectRows(rows, pgx.RowToStructByName[S3Object])
}

// HasS3ObjectsUnder reports whether any mapping exists at or under relPrefix (a fast "is this folder
// mirrored?" check for the browse/processed listings, cheaper than a per-folder S3 ListDir).
func (s *Store) HasS3ObjectsUnder(ctx context.Context, bucket, prefix, relPrefix string) (bool, error) {
	var exists bool
	err := s.pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM s3_objects
		 WHERE bucket=$1 AND prefix=$2 AND (local_rel=$3 OR local_rel LIKE $4))`,
		bucket, prefix, relPrefix, likeEscape(relPrefix)+"/%").Scan(&exists)
	return exists, err
}

// ListS3ChildDirs returns the distinct immediate child directory names under relPrefix (the local-tree
// view the browse column needs), derived from the mapped local_rel paths.
func (s *Store) ListS3ChildDirs(ctx context.Context, bucket, prefix, relPrefix string) ([]string, error) {
	like := "%"
	depth := 1
	if relPrefix != "" {
		like = likeEscape(relPrefix) + "/%"
		depth = strings.Count(relPrefix, "/") + 2
	}
	rows, err := s.pool.Query(ctx,
		`SELECT DISTINCT split_part(local_rel,'/',$4) AS child FROM s3_objects
		 WHERE bucket=$1 AND prefix=$2 AND local_rel LIKE $3 AND split_part(local_rel,'/',$4) <> ''`,
		bucket, prefix, like, depth)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// S3KeyOwners returns, for the given classified keys, the local_rel each is already mapped to — so the
// upload planner can detect a collision (a key already owned by a DIFFERENT file, e.g. two sessions'
// identically-named calibration dirs) and disambiguate that set with the session date.
func (s *Store) S3KeyOwners(ctx context.Context, bucket, prefix string, keys []string) (map[string]string, error) {
	if len(keys) == 0 {
		return map[string]string{}, nil
	}
	rows, err := s.pool.Query(ctx,
		`SELECT s3_key, local_rel FROM s3_objects WHERE bucket=$1 AND prefix=$2 AND s3_key = ANY($3)`,
		bucket, prefix, keys)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, rel string
		if err := rows.Scan(&k, &rel); err != nil {
			return nil, err
		}
		out[k] = rel
	}
	return out, rows.Err()
}

// DeleteS3ObjectsByKeyPrefix removes the ledger rows for keys under a deleted S3 prefix (explorer delete),
// returning how many were pruned.
func (s *Store) DeleteS3ObjectsByKeyPrefix(ctx context.Context, bucket, keyPrefix string) (int64, error) {
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM s3_objects WHERE bucket=$1 AND (s3_key=$2 OR s3_key LIKE $3)`,
		bucket, keyPrefix, likeEscape(keyPrefix)+"/%")
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// RekeyS3Objects rewrites the S3 key of every mapping at or under srcKeyPrefix to sit under dstKeyPrefix
// instead, preserving the sub-path — the ledger half of the explorer's "move" (the physical copy+delete is
// s3store). It updates only s3_key; local_rel (the file's stable local identity, which every inspector /
// serving / staging lookup resolves) is left untouched, so a moved file is still found. Matches on
// (bucket, s3_key) like DeleteS3ObjectsByKeyPrefix, since the explorer works in full keys with no user
// prefix. Returns how many rows were rekeyed.
func (s *Store) RekeyS3Objects(ctx context.Context, bucket, srcKeyPrefix, dstKeyPrefix string) (int64, error) {
	now := nowMs()
	tag, err := s.pool.Exec(ctx,
		`UPDATE s3_objects
		 SET s3_key = $2 || substr(s3_key, length($3) + 1), updated_at = $4
		 WHERE bucket = $1 AND (s3_key = $3 OR s3_key LIKE $5)`,
		bucket, dstKeyPrefix, srcKeyPrefix, now, likeEscape(srcKeyPrefix)+"/%")
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// likeEscape escapes the LIKE metacharacters in a literal path prefix so a "%" or "_" in a folder name is
// matched literally (the default '\' escape character is used).
func likeEscape(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `%`, `\%`)
	return strings.ReplaceAll(s, `_`, `\_`)
}
