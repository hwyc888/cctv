package cctvwasm

import (
	"context"
	"io"
	"net/http"
	"os"
	"strings"
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

func TestExtractModernPlayerModule(t *testing.T) {
	// The 2026 CCTV player uses M instead of wasmBinaryFile and N-based
	// lookup offsets. Test the parser without downloading official content.
	source := []byte("var M=\"data:application/octet-stream;base64,AGFzbQEAAAA=\";" +
		"La.push(function(){F.set([" + strings.Repeat("1,", 1023) + "1],N);" +
		"var A=[];A=A.concat([0,4]);for(var B=0;B<A.length;B++)L[N+A[B]>>2]+=N")
	layout := modernLayout
	wasm, err := extractWASM(source)
	if err != nil || len(wasm) < 8 {
		t.Fatalf("extract modern wasm: %v", err)
	}
	table, err := extractLookupTable(source, layout)
	if err != nil || len(table) != 1024 {
		t.Fatalf("extract modern lookup table: %v (size %d)", err, len(table))
	}
	offsets, err := extractLookupRelocations(source, len(table), true)
	if err != nil || len(offsets) != 2 || offsets[1] != 4 {
		t.Fatalf("extract modern relocations: %v (offsets %v)", err, offsets)
	}
}

func TestRejectUnknownPlayerModule(t *testing.T) {
	_, err := extractWASM([]byte("<html>redirect</html>"))
	if err == nil {
		t.Fatal("expected an error when the source is not a supported CCTV worker")
	}
}
