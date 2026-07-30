package api

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/verove-jordan/astronomy/internal/store"
)

// relForData is the crux of the "Réutiliser" fix: the folder rel returned by /api/processed must be the
// DataDir-relative slash path (the ledger key the S3 pull uses), and must refuse a path outside DataDir.
func TestRelForData(t *testing.T) {
	dataAbs, _ := filepath.Abs("/data")
	cases := []struct {
		name string
		abs  string
		want string
	}{
		{"nested capture folder", "/data/M92/darks", "M92/darks"},
		{"top-level object folder", "/data/M101", "M101"},
		{"deep nested", "/data/M92/session1/flats", "M92/session1/flats"},
		{"outside DataDir", "/other/x", ""},
		{"sibling prefix is not under DataDir", "/data-else/x", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := relForData(dataAbs, c.abs); got != c.want {
				t.Fatalf("relForData(%q, %q) = %q, want %q", dataAbs, c.abs, got, c.want)
			}
		})
	}
}

// buildProcessedGroup backs the single-job /api/processed?job_id= path (the task-detail page). It must map a
// job's params paths → a group (skipping the synthetic s3:// key), mark each folder's local existence, and
// report the locally-absent rels only when a bucket is in play. ok=false for a job that processed no real
// folder, so it never becomes an empty group in the response.
func TestBuildProcessedGroup(t *testing.T) {
	dataAbs, _ := filepath.Abs("/data")
	// exists: only /data/M92/darks is still on local disk; /data/M92/flats was freed after an S3 push.
	exists := func(p string) bool { return p == "/data/M92/darks" }

	t.Run("multi-folder, one freed, bucket set", func(t *testing.T) {
		j := store.Job{
			ID:     42,
			Kind:   "deepsky",
			Status: "succeeded",
			Params: json.RawMessage(`{"paths":["/data/M92/darks","/data/M92/flats","s3://live/x"],"mode":"deepsky","format":"fits"}`),
			Result: json.RawMessage(`{"object":"M92"}`),
		}
		g, absent, ok := buildProcessedGroup(j, exists, dataAbs, "my-bucket")
		if !ok {
			t.Fatal("expected ok=true")
		}
		if g.JobID != 42 || g.Object != "M92" || g.Mode != "deepsky" || g.Format != "fits" {
			t.Fatalf("group meta wrong: %+v", g)
		}
		if len(g.Paths) != 2 { // the s3:// synthetic key is skipped
			t.Fatalf("want 2 real folders, got %d: %+v", len(g.Paths), g.Paths)
		}
		if !g.Paths[0].Local || g.Paths[0].Rel != "M92/darks" {
			t.Fatalf("darks should be local at rel M92/darks: %+v", g.Paths[0])
		}
		if g.Paths[1].Local || g.Paths[1].Rel != "M92/flats" {
			t.Fatalf("flats should be non-local at rel M92/flats: %+v", g.Paths[1])
		}
		if len(absent) != 1 || absent[0] != "M92/flats" {
			t.Fatalf("want absent=[M92/flats], got %v", absent)
		}
		// The signature covers the REAL folders only (s3:// skipped) and is the backend-computed
		// join key the frontend matches saved selections against.
		if want := store.SelectionSignature([]string{"/data/M92/darks", "/data/M92/flats"}); g.Signature != want {
			t.Fatalf("signature = %q, want %q", g.Signature, want)
		}
	})

	t.Run("no bucket → no absent rels collected", func(t *testing.T) {
		j := store.Job{ID: 7, Params: json.RawMessage(`{"path":"/data/M92/flats"}`)}
		_, absent, ok := buildProcessedGroup(j, exists, dataAbs, "")
		if !ok {
			t.Fatal("expected ok=true")
		}
		if len(absent) != 0 {
			t.Fatalf("no bucket → no absent rels, got %v", absent)
		}
	})

	t.Run("only synthetic paths → ok=false", func(t *testing.T) {
		j := store.Job{ID: 9, Params: json.RawMessage(`{"paths":["s3://live/x"]}`)}
		if _, _, ok := buildProcessedGroup(j, exists, dataAbs, "my-bucket"); ok {
			t.Fatal("a job with only a synthetic s3:// path must not produce a group")
		}
	})
}
