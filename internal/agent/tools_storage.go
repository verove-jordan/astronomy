package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"time"

	"github.com/verove-jordan/astronomy/internal/job"
)

// registerStorageTools exposes S3 transfer and backup/restore of precious local state. All are Mutating
// (they enqueue jobs that move data), so each is gated behind a user confirmation.
func registerStorageTools(r *Registry, d Deps) {
	r.Add(Tool{
		Name: "s3_transfer", Category: "storage", Mutating: true,
		Description: "Transfer a folder to/from S3: op upload|sync|download|removeLocal, namespace data (captures) or output (results), rel_path relative to that root.",
		Schema: objectSchema([]string{"op", "bucket", "rel_path"}, map[string]any{
			"op":        strProp("upload|sync|download|removeLocal"),
			"bucket":    strProp("S3 bucket"),
			"prefix":    strProp("S3 key prefix (optional)"),
			"namespace": strProp("data|output (default data)"),
			"rel_path":  strProp("folder path relative to the namespace root"),
		}),
		Handler: func(ctx context.Context, args json.RawMessage) (string, error) { return s3Transfer(ctx, d, args) },
	})
	r.Add(Tool{
		Name: "backup_create", Category: "storage", Mutating: true,
		Description: "Back up precious local state to S3: components db, library, atlas (appstate must be exported from the browser).",
		Schema: objectSchema([]string{"bucket"}, map[string]any{
			"bucket":     strProp("S3 bucket"),
			"prefix":     strProp("S3 key prefix (optional)"),
			"components": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "db|library|atlas (default all three)"},
		}),
		Handler: func(ctx context.Context, args json.RawMessage) (string, error) { return backupCreate(ctx, d, args) },
	})
	r.Add(Tool{
		Name: "backup_restore", Category: "storage", Mutating: true,
		Description: "Restore a backup's components (db, library, atlas) from S3 by its stamp.",
		Schema: objectSchema([]string{"bucket", "stamp"}, map[string]any{
			"bucket":     strProp("S3 bucket"),
			"prefix":     strProp("S3 key prefix (optional)"),
			"stamp":      strProp("the backup stamp to restore"),
			"components": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "db|library|atlas"},
		}),
		Handler: func(ctx context.Context, args json.RawMessage) (string, error) { return backupRestore(ctx, d, args) },
	})
}

func s3Transfer(ctx context.Context, d Deps, args json.RawMessage) (string, error) {
	var in struct {
		Op        string `json:"op"`
		Bucket    string `json:"bucket"`
		Prefix    string `json:"prefix"`
		Namespace string `json:"namespace"`
		RelPath   string `json:"rel_path"`
	}
	if err := decodeArgs(args, &in); err != nil {
		return "", err
	}
	switch in.Op {
	case "upload", "sync", "download", "removeLocal":
	default:
		return "", fmt.Errorf("op must be upload, sync, download or removeLocal")
	}
	if in.Bucket == "" {
		return "", fmt.Errorf("bucket is required")
	}
	if in.Namespace == "" {
		in.Namespace = "data"
	}
	root := d.Cfg.DataDir
	if in.Namespace == "output" {
		root = d.Cfg.OutputDir
	} else if in.Namespace != "data" {
		return "", fmt.Errorf("namespace must be data or output")
	}
	path, err := confinePath(filepath.Join(root, in.RelPath), root)
	if err != nil {
		return "", err
	}
	req := job.RunRequest{
		Path: path, Mode: "transfer",
		Transfer: &job.TransferRequest{Op: in.Op, Bucket: in.Bucket, Prefix: in.Prefix, Namespace: in.Namespace, RelPath: in.RelPath},
	}
	id, err := d.Mgr.Enqueue(ctx, req)
	if err != nil {
		return "", err
	}
	return jsonResult(map[string]any{"job_id": id, "op": in.Op})
}

func backupCreate(ctx context.Context, d Deps, args json.RawMessage) (string, error) {
	var in struct {
		Bucket     string   `json:"bucket"`
		Prefix     string   `json:"prefix"`
		Components []string `json:"components"`
	}
	if err := decodeArgs(args, &in); err != nil {
		return "", err
	}
	if in.Bucket == "" {
		return "", fmt.Errorf("bucket is required")
	}
	if len(in.Components) == 0 {
		in.Components = []string{"db", "library", "atlas"}
	}
	req := job.RunRequest{
		Path: "backup", Mode: "backup",
		Backup: &job.BackupRequest{Bucket: in.Bucket, Prefix: in.Prefix, Components: in.Components, StampMs: time.Now().UnixMilli()},
	}
	id, err := d.Mgr.Enqueue(ctx, req)
	if err != nil {
		return "", err
	}
	return jsonResult(map[string]any{"job_id": id, "components": in.Components})
}

func backupRestore(ctx context.Context, d Deps, args json.RawMessage) (string, error) {
	var in struct {
		Bucket     string   `json:"bucket"`
		Prefix     string   `json:"prefix"`
		Stamp      string   `json:"stamp"`
		Components []string `json:"components"`
	}
	if err := decodeArgs(args, &in); err != nil {
		return "", err
	}
	if in.Bucket == "" || in.Stamp == "" {
		return "", fmt.Errorf("bucket and stamp are required")
	}
	req := job.RunRequest{
		Path: "restore", Mode: "restore",
		Restore: &job.RestoreRequest{Bucket: in.Bucket, Prefix: in.Prefix, Stamp: in.Stamp, Components: in.Components},
	}
	id, err := d.Mgr.Enqueue(ctx, req)
	if err != nil {
		return "", err
	}
	return jsonResult(map[string]any{"job_id": id})
}
