//go:build integration

package transport

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	admin "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/administration/adapters"
	experiment "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/experiment/adapters"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/projectaccess"
)

func TestExportArchiveAndLimits(t *testing.T) {
	f := newExportFixture(t, false)
	server := httptest.NewServer(f.s)
	defer server.Close()
	r := f.request()
	r.URL.Scheme = "http"
	r.URL.Host = strings.TrimPrefix(server.URL, "http://")
	r.RequestURI = ""
	response, err := server.Client().Do(r)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || response.StatusCode != 200 || response.Header.Get("Content-Length") != strconv.Itoa(len(data)) {
		t.Fatal("incomplete export", response.StatusCode, err)
	}
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil || len(archive.File) != 8 {
		t.Fatal("archive contents", err)
	}
	files := map[string][]byte{}
	for _, entry := range archive.File {
		reader, e := entry.Open()
		if e != nil {
			t.Fatal(e)
		}
		raw, e := io.ReadAll(reader)
		reader.Close()
		if e != nil {
			t.Fatal(e)
		}
		files[entry.Name] = raw
	}
	if !bytes.Equal(files["final_model.bin"], f.model) || !bytes.Contains(files["metrics.csv"], []byte("0.875")) || !json.Valid(files["run_manifest.json"]) || bytes.Contains(files["models.json"], []byte("private/export")) {
		t.Fatal("export changed immutable result or leaked storage key")
	}
	if !bytes.Contains(files["validation_accuracy.svg"], []byte("30.000,60.000")) || !json.Valid(files["confusions.json"]) || !json.Valid(files["config.json"]) {
		t.Fatal("derived data or JSON missing")
	}
	calls := 0
	f.s.Experiments.Blobs = exportBlobFunc(func(context.Context, string, string, int64) ([]byte, error) { calls++; return f.model, nil })
	authorizationExec(t, f.pool, "UPDATE artifacts SET size_bytes=$2 WHERE id=$1", f.artifact, experiment.ExportModelLimit+1)
	_, err = f.s.Experiments.Export(context.Background(), f.access, f.run)
	projectFailure(t, err, 409, "EXPORT_LIMIT_EXCEEDED")
	if calls != 0 {
		t.Fatal("oversized model fetched")
	}
	authorizationExec(t, f.pool, "UPDATE artifacts SET size_bytes=$2 WHERE id=$1", f.artifact, len(f.model))
	authorizationExec(t, f.pool, "UPDATE runs SET manifest_json=jsonb_build_object('padding',repeat('x',$2::int)) WHERE id=$1", f.run, experiment.ExportMetadataLimit)
	_, err = f.s.Experiments.Export(context.Background(), f.access, f.run)
	projectFailure(t, err, 409, "EXPORT_LIMIT_EXCEEDED")
	if calls != 0 {
		t.Fatal("oversized metadata fetched model")
	}
	authorizationExec(t, f.pool, `UPDATE runs SET manifest_json='{"rounds":1,"partition_manifest_hash":"fixture"}' WHERE id=$1`, f.run)
	f.s.Experiments.Blobs = exportBlobFunc(func(context.Context, string, string, int64) ([]byte, error) { return []byte("corrupt"), nil })
	projectHTTP(t, f.authorizationFixture, "owner", "GET", "runs/"+f.run+"/export", "", 503, "DEPENDENCY_UNAVAILABLE")
	if f.s.exportBusy.Load() {
		t.Fatal("failed export retained flight")
	}
	f.s.Experiments.Blobs = exportBlobFunc(func(context.Context, string, string, int64) ([]byte, error) { return f.model, nil })
	// A recorder lacks write deadlines: reject before ZIP headers, then release flight.
	projectHTTP(t, f.authorizationFixture, "owner", "GET", "runs/"+f.run+"/export", "", 503, "DEPENDENCY_UNAVAILABLE")
	if f.s.exportBusy.Load() {
		t.Fatal("unsupported writer retained flight")
	}
}

func TestExportPausedBlobRevocation(t *testing.T) {
	for _, change := range []string{"logout", "password", "disable", "grant"} {
		t.Run(change, func(t *testing.T) {
			f := newExportFixture(t, change == "grant")
			var credentialService admin.Service
			if change == "password" {
				credentialService = configureCredentialAdministration(t, f.s)
			}
			entered, resume := make(chan struct{}), make(chan struct{})
			defer close(resume)
			f.s.Experiments.Blobs = exportBlobFunc(func(ctx context.Context, _ string, _ string, _ int64) ([]byte, error) {
				close(entered)
				select {
				case <-resume:
					return f.model, nil
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			})
			w := httptest.NewRecorder()
			done := make(chan struct{})
			go func() { defer close(done); f.s.export(w, f.request(), f.access, f.run) }()
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("snapshot did not reach blob")
			}
			// The first real request holds the flight while S3 is paused.
			busy := httptest.NewRecorder()
			f.s.export(busy, f.request(), f.access, f.run)
			var busyError struct {
				Code      string `json:"code"`
				Retryable bool   `json:"retryable"`
			}
			if json.Unmarshal(busy.Body.Bytes(), &busyError) != nil || busy.Code != 503 || busyError.Code != "EXPORT_BUSY" || !busyError.Retryable || busy.Header().Get("Retry-After") != "3" || !f.s.exportBusy.Load() {
				t.Fatalf("concurrent export did not preserve active flight: %d %s", busy.Code, busy.Body.String())
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			var err error
			store := admin.Store{Pool: f.pool}
			switch change {
			case "logout":
				err = f.s.Identity.Logout(ctx, f.users["owner"].ID)
			case "password":
				_, err = credentialService.MutateCredential(ctx, f.users["owner"].Principal(), administrationCredentialCommand("password.change", "", "export-password", strings.Repeat("c", 64), admin.Input{CurrentPassword: "valid-old-password", Password: "valid-new-password"}))
			case "disable":
				disabled := true
				_, err = store.Mutate(ctx, f.users["admin"].ID, "user.update", f.users["owner"].ID, "export-disable", strings.Repeat("d", 64), admin.Input{Role: "researcher", Disabled: &disabled})
			case "grant":
				_, err = (projectaccess.Store{Pool: f.pool}).SaveGrant(ctx, f.users["other"].ID, f.org, f.project, f.users["owner"].ID, projectaccess.GrantInput{Role: "viewer", Status: "revoked", Revision: 0})
			}
			if err != nil {
				t.Fatalf("revocation blocked while S3 paused: %v", err)
			}
			resume <- struct{}{}
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("export did not finish after revoke")
			}
			want := 401
			if change == "grant" {
				want = 404
			}
			if w.Code != want || w.Header().Get("Content-Type") != "application/json" || bytes.HasPrefix(w.Body.Bytes(), []byte("PK")) || f.s.exportBusy.Load() {
				t.Fatalf("revoked export leaked archive or flight: %d %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestExportMidstreamLogoutAborts(t *testing.T) {
	f := newExportFixture(t, false)
	w := &exportTestWriter{ResponseRecorder: httptest.NewRecorder()}
	w.afterWrite = func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := f.s.Identity.Logout(ctx, f.users["owner"].ID); err != nil {
			t.Fatal("write held auth guard", err)
		}
		w.afterWrite = nil
	}
	var recovered any
	func() { defer func() { recovered = recover() }(); f.s.ServeHTTP(w, f.request()) }()
	if recovered != http.ErrAbortHandler || w.Body.Len() != exportChunkSize || f.s.metrics.abortedExports.Load() != 1 || f.s.metrics.classes[2].Load() != 1 || f.s.exportBusy.Load() {
		t.Fatalf("abort accounting/body: %v %d", recovered, w.Body.Len())
	}
	if w.Header().Get("Content-Length") == strconv.Itoa(w.Body.Len()) {
		t.Fatal("aborted response advertised complete body")
	}
}
