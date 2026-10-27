package cctvwasm

import (
	"context"
	"io"
	"net/http"
	"os"
	"testing"
	"time"
)

func TestOfficialModuleStartsWithoutBrowser(t *testing.T) {
	path := os.Getenv("CCTV_WASM_SOURCE")
	if path == "" {
		t.Skip("set CCTV_WASM_SOURCE to cctv.worker.js")
	}
	source, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	engine, err := NewEngine(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close(ctx)
	session, err := engine.NewSession(ctx, "go-wasm-test", "https://tv.cctv.com/")
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close(ctx)
	if err := session.Update(); err != nil {
		t.Fatal(err)
	}
}

func TestLiveSegmentWithoutBrowser(t *testing.T) {
	sourcePath := os.Getenv("CCTV_WASM_SOURCE")
	segmentURL := os.Getenv("CCTV_TS_URL")
	if sourcePath == "" || segmentURL == "" {
		t.Skip("set CCTV_WASM_SOURCE and CCTV_TS_URL")
	}
	source, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodGet, segmentURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("User-Agent", "Mozilla/5.0")
	request.Header.Set("Referer", "https://tv.cctv.com/")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	input, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	engine, err := NewEngine(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close(ctx)
	started := time.Now()
	output, err := engine.DecryptMPEGTS(ctx, input, "1786200000000-0", "http://tv.cctv.com:3010")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("pure Go decrypted segment: input=%d output=%d elapsed=%s", len(input), len(output), time.Since(started))
	if outputPath := os.Getenv("CCTV_TS_OUTPUT"); outputPath != "" {
		if err := os.WriteFile(outputPath, output, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}
