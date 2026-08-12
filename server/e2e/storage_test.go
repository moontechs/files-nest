//go:build e2e

// Package e2e provides black-box end-to-end tests for the iCloud Backup
// server. This file adds on-disk verification: it reaches into the running
// container's filesystem (via `docker compose cp`/`exec`) to confirm that
// completed uploads are byte-for-byte identical to what was sent, and that
// DELETE actually removes the organized file from disk. Without this, the
// suite only ever checks HTTP responses and never opens the files the
// server claims to have written.
//
// These checks are opt-in: they only run when COMPOSE_PROJECT_NAME (or
// E2E_COMPOSE_PROJECT) is set and the docker CLI is available, since the
// rest of the e2e suite is designed to run against any SERVER_URL —
// including a remote deployment with no local compose stack to inspect.
// `make e2e` / `make e2e-test` export COMPOSE_PROJECT_NAME automatically,
// so the checks run by default in the normal local/CI flow.
package e2e

import (
	"errors"
	"os"
	"os/exec"
	"path"
	"testing"

	"github.com/stretchr/testify/require"
)

// containerStoragePath is the STORAGE_PATH configured for the server
// container in docker-compose.e2e.yml.
const containerStoragePath = "/data"

// storageAccess holds the coordinates needed to reach into the e2e stack's
// server container filesystem via the docker compose CLI.
type storageAccess struct {
	composeFile string
	project     string
	service     string
}

// requireDockerStorage returns a storageAccess for the running e2e stack,
// or skips the calling test when disk verification isn't possible: no
// docker CLI on PATH, or no compose project configured (e.g. SERVER_URL
// points at a deployment with no local stack to inspect).
func requireDockerStorage(t testing.TB) storageAccess {
	t.Helper()

	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker CLI not available — skipping on-disk verification")
	}

	project := os.Getenv("E2E_COMPOSE_PROJECT")
	if project == "" {
		project = os.Getenv("COMPOSE_PROJECT_NAME")
	}
	if project == "" {
		t.Skip("COMPOSE_PROJECT_NAME/E2E_COMPOSE_PROJECT not set — skipping on-disk verification (no local stack to inspect)")
	}

	composeFile := os.Getenv("E2E_COMPOSE_FILE")
	if composeFile == "" {
		// go test ./e2e/... runs with cwd server/e2e/, one level below
		// where docker-compose.e2e.yml actually lives (server/).
		composeFile = "../docker-compose.e2e.yml"
	}

	service := os.Getenv("E2E_STORAGE_SERVICE")
	if service == "" {
		service = "server"
	}

	return storageAccess{composeFile: composeFile, project: project, service: service}
}

// readFile copies organizedPath (relative to STORAGE_PATH, e.g.
// "organized/2026/07/22/IMG_1234.jpg") out of the running container and
// returns its raw bytes.
func (s storageAccess) readFile(t testing.TB, organizedPath string) []byte {
	t.Helper()

	remote := path.Join(containerStoragePath, organizedPath)

	tmp, err := os.CreateTemp("", "e2e-storage-*")
	require.NoError(t, err, "create temp file for docker compose cp")
	tmpPath := tmp.Name()
	tmp.Close()
	defer os.Remove(tmpPath)

	cmd := exec.Command("docker", "compose", "-p", s.project, "-f", s.composeFile,
		"cp", s.service+":"+remote, tmpPath)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "docker compose cp %s: %s", remote, string(out))

	data, err := os.ReadFile(tmpPath)
	require.NoError(t, err, "read copied file %s", tmpPath)
	return data
}

// fileExists reports whether organizedPath (relative to STORAGE_PATH)
// exists in the running container.
func (s storageAccess) fileExists(t testing.TB, organizedPath string) bool {
	t.Helper()

	remote := path.Join(containerStoragePath, organizedPath)

	cmd := exec.Command("docker", "compose", "-p", s.project, "-f", s.composeFile,
		"exec", "-T", s.service, "test", "-e", remote)
	err := cmd.Run()
	if err == nil {
		return true
	}

	// `test -e` exits 1 when the path does not exist; any other failure
	// (compose/docker error, container gone, etc.) is a real test failure.
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return false
	}
	require.NoError(t, err, "docker compose exec test -e %s", remote)
	return false
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestStorage_CompletedFileContentMatchesUploadedBytes verifies that the
// organized file on disk is byte-for-byte identical to what was uploaded —
// not just that the API reports a non-empty organized_path.
func TestStorage_CompletedFileContentMatchesUploadedBytes(t *testing.T) {
	storage := requireDockerStorage(t)

	localID := MakeLocalIdentifier(t, t.Name())

	payload := make([]byte, 8192)
	for i := range payload {
		payload[i] = byte((i*31 + 7) % 256)
	}

	rec := CreateCompleteUpload(t, localID, "IMG_disk_check.jpg", payload)
	require.NotEmpty(t, rec.OrganizedPath)

	onDisk := storage.readFile(t, rec.OrganizedPath)
	require.Equal(t, payload, onDisk,
		"organized file content on disk must match the bytes that were uploaded")
}

// TestStorage_DeleteRemovesFileFromDisk verifies that DELETE /uploads/:id
// actually removes the organized file from disk, per the documented
// invariant that deletion is permanent and unrecoverable.
func TestStorage_DeleteRemovesFileFromDisk(t *testing.T) {
	storage := requireDockerStorage(t)

	localID := MakeLocalIdentifier(t, t.Name())
	rec := CreateCompleteUpload(t, localID, "IMG_delete_check.jpg", []byte("data-to-be-deleted"))
	require.NotEmpty(t, rec.OrganizedPath)

	require.True(t, storage.fileExists(t, rec.OrganizedPath),
		"organized file must exist on disk right after completion")

	MustDeleteUpload(t, rec.ID)

	require.False(t, storage.fileExists(t, rec.OrganizedPath),
		"organized file must be removed from disk after DELETE")
}
