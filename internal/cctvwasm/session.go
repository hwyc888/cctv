package cctvwasm

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

var vodFunctions = []string{"ta", "sa", "ra", "qa", "pa", "oa", "na", "ma"}

// Session is a fresh official CCTV decryption state. CCTV's player requires a
// new state for each TS segment; reusing it can corrupt decoded frames.
type Session struct {
	module      api.Module
	environment api.Module
	engine      *Engine
	host        *hostSession
	ctx         context.Context
	mediaTagID  string
	vmpTag      string
}

func (e *Engine) NewSession(ctx context.Context, mediaTagID, activeURL string) (*Session, error) {
	e.sessionMu.Lock()
	success := false
	defer func() {
		if !success {
			e.sessionMu.Unlock()
		}
	}()
	origin := strings.TrimRight(activeURL, "/")
	if origin == "" {
		origin = "https://tv.cctv.com"
	}
	host := &hostSession{
		activeURL:   origin,
		locationURL: origin + "/official-transmux-worker.js",
		handles:     make(map[uint32]any),
		handleRefs:  make(map[uint32]uint32),
		types:       make(map[uint32]emvalType),
	}
	ctx = context.WithValue(ctx, sessionContextKey{}, host)
	environment, err := e.runtime.InstantiateModule(ctx, e.compiledEnvironment, wazero.NewModuleConfig().WithName("env").WithStartFunctions())
	if err != nil {
		return nil, fmt.Errorf("实例化央视 WASM 环境: %w", err)
	}
	memory := environment.Memory()
	// Keep allocations above the relocated lookup table. The generated loader
	// reserves this range before the exported allocator is used.
	nextDynamic := lookupTableBase + uint32(len(e.table))
	nextDynamic = (nextDynamic + 15) &^ 15
	if err := writeUint32(memory, dynamicTopPointer, nextDynamic); err != nil {
		_ = environment.Close(ctx)
		return nil, err
	}
	name := fmt.Sprintf("cctv-vod-%d", e.nextID.Add(1))
	module, err := e.runtime.InstantiateModule(ctx, e.compiled, wazero.NewModuleConfig().WithName(name).WithStartFunctions())
	if err != nil {
		_ = environment.Close(ctx)
		return nil, fmt.Errorf("实例化央视 WASM: %w", err)
	}
	session := &Session{module: module, environment: environment, engine: e, host: host, ctx: ctx, mediaTagID: mediaTagID}
	if !memory.Write(lookupTableBase, e.table) {
		_ = module.Close(ctx)
		_ = environment.Close(ctx)
		return nil, fmt.Errorf("央视 WASM 查找表写入越界: %d", len(e.table))
	}
	for _, offset := range e.relocations {
		pointer := lookupTableBase + offset
		value, ok := memory.ReadUint32Le(pointer)
		if !ok || !memory.WriteUint32Le(pointer, value+lookupTableBase) {
			_ = module.Close(ctx)
			_ = environment.Close(ctx)
			return nil, fmt.Errorf("央视 WASM 重定位越界: %d", offset)
		}
	}
	if _, err := module.ExportedFunction("La").Call(ctx); err != nil {
		_ = module.Close(ctx)
		_ = environment.Close(ctx)
		return nil, fmt.Errorf("初始化央视 WASM 运行库: %w", err)
	}
	if _, err := session.callString("aa", mediaTagID, true); err != nil {
		_ = module.Close(ctx)
		_ = environment.Close(ctx)
		return nil, fmt.Errorf("初始化央视播放器会话: %w", err)
	}
	// The generated loader invokes onRuntimeInitialized (where the worker
	// initializes the player) before it falls through to callMain.
	if err := runModuleMain(ctx, module); err != nil {
		_ = module.Close(ctx)
		_ = environment.Close(ctx)
		return nil, fmt.Errorf("启动央视 WASM 运行库: %w", err)
	}
	if err := session.completePendingFetches(); err != nil {
		_ = module.Close(ctx)
		_ = environment.Close(ctx)
		return nil, fmt.Errorf("完成央视播放器辅助请求: %w", err)
	}
	success = true
	return session, nil
}

func runModuleMain(ctx context.Context, module api.Module) error {
	// Emscripten's generated run() invokes main with a single program-name
	// argument after global constructors. Besides initializing the library,
	// its two stackAlloc calls are observable by CCTV's address-dependent VOD
	// transform and therefore must be reproduced exactly.
	argumentVectorResult, err := module.ExportedFunction("Ma").Call(ctx, 8)
	if err != nil || len(argumentVectorResult) == 0 {
		return fmt.Errorf("分配参数向量: %w", err)
	}
	const programName = "./this.program"
	programResult, err := module.ExportedFunction("Ma").Call(ctx, uint64(len(programName)+1))
	if err != nil || len(programResult) == 0 {
		return fmt.Errorf("分配程序名称: %w", err)
	}
	argumentVector := uint32(argumentVectorResult[0])
	programPointer := uint32(programResult[0])
	memory := module.Memory()
	if !memory.Write(programPointer, append([]byte(programName), 0)) ||
		!memory.WriteUint32Le(argumentVector, programPointer) ||
		!memory.WriteUint32Le(argumentVector+4, 0) {
		return fmt.Errorf("写入程序参数越界")
	}
	if _, err := module.ExportedFunction("Ka").Call(ctx); err != nil {
		return fmt.Errorf("保存央视 EMT 栈: %w", err)
	}
	if _, err := module.ExportedFunction("Ea").Call(ctx, 1, uint64(argumentVector)); err != nil {
		return fmt.Errorf("执行央视 WASM main: %w", err)
	}
	return nil
}

func (s *Session) completePendingFetches() error {
	for _, fetchPointer := range s.host.pendingFetches {
		// The official worker's auxiliary URL resolves to its normal 404 body.
		// Emscripten places that response in WASM memory before invoking the
		// registered error callback on the next event-loop turn.
		const responseBody = "not found"
		results, err := s.module.ExportedFunction("Fa").Call(s.ctx, uint64(len(responseBody)))
		if err != nil || len(results) == 0 || results[0] == 0 {
			return fmt.Errorf("分配辅助响应: %w", err)
		}
		responsePointer := uint32(results[0])
		memory := s.module.Memory()
		if !memory.Write(responsePointer, []byte(responseBody)) ||
			!memory.WriteUint32Le(fetchPointer+12, responsePointer) ||
			!memory.WriteUint64Le(fetchPointer+16, uint64(len(responseBody))) ||
			!memory.WriteUint64Le(fetchPointer+24, 0) ||
			!memory.WriteUint64Le(fetchPointer+32, uint64(len(responseBody))) ||
			!memory.WriteUint16Le(fetchPointer+40, 4) ||
			!memory.WriteUint16Le(fetchPointer+42, 404) ||
			!memory.Write(fetchPointer+44, append([]byte("Not Found"), 0)) {
			return fmt.Errorf("写入辅助响应越界")
		}
		callback, ok := memory.ReadUint32Le(fetchPointer + 112 + 40)
		if !ok {
			return fmt.Errorf("读取辅助请求回调越界")
		}
		if callback != 0 {
			if _, err := s.module.ExportedFunction("Ia").Call(s.ctx, uint64(callback), uint64(fetchPointer)); err != nil {
				return fmt.Errorf("执行辅助请求回调: %w", err)
			}
		}
	}
	s.host.pendingFetches = nil
	return nil
}

func (s *Session) callString(functionName, value string, extended bool) (uint32, error) {
	size := uint32(len(value) + 1)
	if extended {
		// moduleActive allocates string length + MemoryExtend; the zero-filled
		// extension itself supplies the C terminator.
		size = uint32(len(value)) + memoryExtend
	}
	pointer, err := s.malloc(size)
	if err != nil {
		return 0, err
	}
	defer s.free(pointer)
	buffer := make([]byte, size)
	copy(buffer, value)
	if !s.module.Memory().Write(pointer, buffer) {
		return 0, fmt.Errorf("写入央视 WASM 字符串越界")
	}
	results, err := s.module.ExportedFunction(functionName).Call(s.ctx, uint64(pointer))
	if err != nil {
		return 0, err
	}
	if len(results) == 0 {
		return 0, nil
	}
	return uint32(results[0]), nil
}

func (s *Session) malloc(size uint32) (uint32, error) {
	results, err := s.module.ExportedFunction("Da").Call(s.ctx, uint64(size))
	if err != nil {
		return 0, err
	}
	if len(results) == 0 || results[0] == 0 {
		return 0, fmt.Errorf("央视 WASM 无法分配 %d 字节", size)
	}
	return uint32(results[0]), nil
}

func (s *Session) free(pointer uint32) {
	if pointer != 0 {
		_, _ = s.module.ExportedFunction("Ca").Call(s.ctx, uint64(pointer))
	}
}

// Update mirrors CNTV_UpdatePlayer and selects the per-frame VOD transform
// order used by the official worker.
func (s *Session) Update() error {
	value, err := s.callString("ca", s.mediaTagID, true)
	if err != nil {
		return err
	}
	if value != 0 {
		s.vmpTag = fmt.Sprintf("%08x", value)
	}
	return nil
}

// DecryptNAL decrypts one H264 NAL unit in place through the official VOD API.
func (s *Session) DecryptNAL(argument string, input []byte) ([]byte, error) {
	dataPointer, err := s.malloc(uint32(len(input)) + memoryExtend)
	if err != nil {
		return nil, err
	}
	if !s.module.Memory().Write(dataPointer, input) {
		s.free(dataPointer)
		return nil, fmt.Errorf("写入央视加密 NAL 越界")
	}
	activeURL := []byte(s.host.activeURL)
	if len(activeURL) > 0 && !s.module.Memory().Write(dataPointer+uint32(len(input)), activeURL) {
		s.free(dataPointer)
		return nil, fmt.Errorf("写入央视播放器来源地址越界")
	}
	argumentPointer, err := s.malloc(uint32(len(argument) + 1))
	if err != nil {
		s.free(dataPointer)
		return nil, err
	}
	cleanup := func() {
		// The official worker frees the data allocation first. Its custom
		// allocator's reuse pattern is part of the VOD module's state machine.
		s.free(dataPointer)
		s.free(argumentPointer)
	}
	if !s.module.Memory().Write(argumentPointer, append([]byte(argument), 0)) {
		cleanup()
		return nil, fmt.Errorf("写入央视解密参数越界")
	}
	parameters := []uint64{
		uint64(argumentPointer),
		uint64(dataPointer),
		uint64(len(input)),
		uint64(len(activeURL)),
	}
	for index, digit := range s.vmpTag {
		if index >= len(vodFunctions) || digit < '0' || digit > '6' {
			continue
		}
		if _, err := s.module.ExportedFunction(vodFunctions[index]).Call(s.ctx, parameters...); err != nil {
			cleanup()
			return nil, fmt.Errorf("央视 VOD 解密阶段 %d: %w", index, err)
		}
	}
	results, err := s.module.ExportedFunction("ua").Call(s.ctx, parameters...)
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("央视 VOD 最终解密（vmpTag=%q）: %w", s.vmpTag, err)
	}
	if len(results) == 0 {
		cleanup()
		return nil, fmt.Errorf("央视 VOD 解密没有返回长度")
	}
	length := uint32(results[0])
	if length > uint32(len(input))+memoryExtend {
		cleanup()
		return nil, fmt.Errorf("央视 VOD 返回长度异常: %d", length)
	}
	output, ok := s.module.Memory().Read(dataPointer, length)
	if !ok {
		cleanup()
		return nil, fmt.Errorf("读取央视 VOD 解密结果越界")
	}
	result := append([]byte(nil), output...)
	cleanup()
	return result, nil
}

func (s *Session) ArgumentForDTS(dts int64) string {
	return s.mediaTagID + "##" + strconv.FormatInt(dts, 10) + "##0"
}

func (s *Session) Close(ctx context.Context) error {
	if s.module == nil {
		return nil
	}
	moduleErr := s.module.Close(ctx)
	environmentErr := s.environment.Close(ctx)
	s.module = nil
	s.environment = nil
	s.engine.sessionMu.Unlock()
	if moduleErr != nil {
		return moduleErr
	}
	return environmentErr
}
