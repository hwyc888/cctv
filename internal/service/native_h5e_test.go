package service

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestNativeH5EHelperRunsAndPreservesSegmentSize(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is specific to Linux CI")
	}
	helper := filepath.Join(t.TempDir(), "h5e-helper")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\ncp \"$1\" \"$2\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	segment := bytes.Repeat([]byte{0x47}, 188*3)
	decrypted, err := nativeH5EDecryptTS(context.Background(), helper, segment)
	if err != nil || !bytes.Equal(decrypted, segment) {
		t.Fatalf("native helper failed: %v", err)
	}
	t.Setenv("CCTV_H5E_DECRYPT_PATH", helper)
	found, err := findNativeH5E()
	if err != nil || found != helper {
		t.Fatalf("native helper path resolution: %s %v", found, err)
	}
}

func TestNativeH5ERejectsInvalidInputAndCanceledRequests(t *testing.T) {
	_, err := nativeH5EDecryptTS(context.Background(), "missing-helper", []byte("not an MPEG-TS segment"))
	if err == nil {
		t.Fatal("expected an invalid segment error")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = nativeH5EDecryptTS(ctx, "missing-helper", make([]byte, 188))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation, got %v", err)
	}
}

func TestNativeH5ERejectsWrongSizeOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is specific to Linux CI")
	}
	helper := filepath.Join(t.TempDir(), "bad-helper")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\nprintf 'bad' > \"$2\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := nativeH5EDecryptTS(context.Background(), helper, make([]byte, 188))
	if err == nil {
		t.Fatal("expected native decoder output size validation to fail")
	}
}
