package app

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestVideoReplicationProbeInterruptedMapsToRetryable503(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), -time.Second)
	defer cancel()
	err := videoReplicationProbeFailure(ctx, errors.New("signal: killed"))
	if !errors.Is(err, errVideoReplicationProbeInterrupted) {
		t.Fatalf("expected interrupted error, got %v", err)
	}
	appErr, ok := videoReplicationProbeUserError(err).(*AppError)
	if !ok || appErr.Status != 503 || !appErr.Retryable {
		t.Fatalf("expected retryable 503, got %#v", videoReplicationProbeUserError(err))
	}
}

func TestVideoReplicationProbeBrokenFileMapsTo400(t *testing.T) {
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe not installed")
	}
	path := filepath.Join(t.TempDir(), "broken.mp4")
	if err := os.WriteFile(path, []byte("not a video at all"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := probeVideoReplicationFile(context.Background(), path)
	if err == nil {
		t.Fatal("expected probe failure")
	}
	if errors.Is(err, errVideoReplicationProbeInterrupted) {
		t.Fatalf("broken file should not be treated as interrupted: %v", err)
	}
	appErr, ok := videoReplicationProbeUserError(err).(*AppError)
	if !ok || appErr.Status != 400 {
		t.Fatalf("expected 400 for broken file, got %#v", videoReplicationProbeUserError(err))
	}
}

func TestVideoReplicationProbeUnavailableMapsTo503(t *testing.T) {
	appErr, ok := videoReplicationProbeUserError(errMediaProbeUnavailable).(*AppError)
	if !ok || appErr.Status != 503 || !appErr.Retryable {
		t.Fatalf("expected retryable 503, got %#v", videoReplicationProbeUserError(errMediaProbeUnavailable))
	}
}
