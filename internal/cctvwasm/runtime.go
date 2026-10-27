package cctvwasm

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	wabinbinary "github.com/tetratelabs/wabin/binary"
	"github.com/tetratelabs/wabin/leb128"
	wabinwasm "github.com/tetratelabs/wabin/wasm"
	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

const (
	dynamicTopPointer = uint32(40512)
	dynamicBase       = uint32(5283424)
	tempDoublePointer = uint32(40528)
	lookupTableBase   = uint32(6332000)
	memoryExtend      = uint32(2048)
)

type moduleLayout struct {
	modern            bool
	dynamicTopPointer uint32
	lookupTableBase   uint32
	globals           map[string]uint32
}

var legacyLayout = moduleLayout{
	dynamicTopPointer: dynamicTopPointer,
	lookupTableBase:   lookupTableBase,
	globals: map[string]uint32{
		"__table_base": 0,
		"a":            tempDoublePointer,
		"b":            dynamicTopPointer,
		"c":            dynamicBase,
		"d":            lookupTableBase,
	},
}

// The official CCTV worker build dated 2026-08-24 changed the Emscripten
// minifier names and grew its static lookup table. The WASM and loader must
// use the same addresses; old cached modules retain the legacy layout.
var modernLayout = moduleLayout{
	modern:            true,
	dynamicTopPointer: 40992,
	lookupTableBase:   6332480,
	globals: map[string]uint32{
		"__table_base": 0,
		"a":            41008,
		"b":            40992,
		"c":            5283904,
		"d":            6332480,
	},
}

type sessionContextKey struct{}

type hostSession struct {
	activeURL      string
	locationURL    string
	tempRet0       uint32
	handles        map[uint32]any
	handleRefs     map[uint32]uint32
	freeHandles    []uint32
	nextHandle     uint32
	types          map[uint32]emvalType
	pendingFetches []uint32
}

type undefinedValue struct{}

type emvalObject map[string]any

type emvalType struct {
	kind string
	size uint32
}

// Engine owns a compiled copy of CCTV's official VOD decryption module.
// Sessions are safe to use from multiple goroutines, but are serialized because
// each one must instantiate the official module under its fixed "env" name.
type Engine struct {
	layout              moduleLayout
	runtime             wazero.Runtime
	compiled            wazero.CompiledModule
	compiledEnvironment wazero.CompiledModule
	table               []byte
	relocations         []uint32
	nextID              atomic.Uint64
	sessionMu           sync.Mutex
}

// NewEngine extracts CCTV's embedded WebAssembly module and its lookup table
// from cctv.worker.js, then compiles them for direct execution in Go.
func NewEngine(ctx context.Context, workerSource []byte) (*Engine, error) {
	layout := legacyLayout
	if bytes.Contains(workerSource, []byte(`var M="data:application/octet-stream;base64,`)) {
		layout = modernLayout
	}
	wasmBytes, err := extractWASM(workerSource)
	if err != nil {
		return nil, err
	}
	lookupTable, err := extractLookupTable(workerSource, layout)
	if err != nil {
		return nil, err
	}
	relocations, err := extractLookupRelocations(workerSource, len(lookupTable), layout.modern)
	if err != nil {
		return nil, err
	}

	decoded, err := wabinbinary.DecodeModule(wasmBytes, wabinwasm.CoreFeaturesV2)
	if err != nil {
		return nil, fmt.Errorf("解析央视 WASM: %w", err)
	}
	environmentWASM, functionImports, err := buildEnvironmentModule(decoded, layout.globals)
	if err != nil {
		return nil, err
	}

	runtimeConfig := wazero.NewRuntimeConfigCompiler().WithCloseOnContextDone(true)
	runtime := wazero.NewRuntimeWithConfig(ctx, runtimeConfig)
	engine := &Engine{runtime: runtime, layout: layout, table: lookupTable, relocations: relocations}
	if err := engine.instantiateHostModule(ctx, decoded, functionImports); err != nil {
		_ = runtime.Close(ctx)
		return nil, err
	}
	engine.compiledEnvironment, err = runtime.CompileModule(ctx, environmentWASM)
	if err != nil {
		_ = runtime.Close(ctx)
		return nil, fmt.Errorf("编译央视 WASM 环境: %w", err)
	}
	engine.compiled, err = runtime.CompileModule(ctx, wasmBytes)
	if err != nil {
		_ = runtime.Close(ctx)
		return nil, fmt.Errorf("编译央视 WASM: %w", err)
	}
	return engine, nil
}

func buildEnvironmentModule(module *wabinwasm.Module, globals map[string]uint32) ([]byte, []*wabinwasm.Import, error) {
	environment := &wabinwasm.Module{TypeSection: module.TypeSection}
	functionImports := make([]*wabinwasm.Import, 0, 50)
	functionIndex := uint32(0)
	globalIndex := uint32(0)
	for _, imp := range module.ImportSection {
		switch imp.Type {
		case wabinwasm.ExternTypeFunc:
			functionImports = append(functionImports, imp)
			environment.ImportSection = append(environment.ImportSection, &wabinwasm.Import{
				Type:     wabinwasm.ExternTypeFunc,
				Module:   "goenv",
				Name:     imp.Name,
				DescFunc: imp.DescFunc,
			})
			environment.ExportSection = append(environment.ExportSection, &wabinwasm.Export{
				Type: wabinwasm.ExternTypeFunc, Name: imp.Name, Index: functionIndex,
			})
			functionIndex++
		case wabinwasm.ExternTypeGlobal:
			value, ok := globals[imp.Name]
			if !ok {
				return nil, nil, fmt.Errorf("央视 WASM 出现未知全局变量 %q", imp.Name)
			}
			environment.GlobalSection = append(environment.GlobalSection, &wabinwasm.Global{
				Type: imp.DescGlobal,
				Init: &wabinwasm.ConstantExpression{
					Opcode: wabinwasm.OpcodeI32Const,
					Data:   leb128.EncodeInt32(int32(value)),
				},
			})
			environment.ExportSection = append(environment.ExportSection, &wabinwasm.Export{
				Type: wabinwasm.ExternTypeGlobal, Name: imp.Name, Index: globalIndex,
			})
			globalIndex++
		case wabinwasm.ExternTypeMemory:
			environment.MemorySection = imp.DescMem
			environment.ExportSection = append(environment.ExportSection, &wabinwasm.Export{
				Type: wabinwasm.ExternTypeMemory, Name: imp.Name, Index: 0,
			})
		case wabinwasm.ExternTypeTable:
			environment.TableSection = []*wabinwasm.Table{imp.DescTable}
			environment.ExportSection = append(environment.ExportSection, &wabinwasm.Export{
				Type: wabinwasm.ExternTypeTable, Name: imp.Name, Index: 0,
			})
		default:
			return nil, nil, fmt.Errorf("央视 WASM 出现未知导入类型 %d", imp.Type)
		}
	}
	if environment.MemorySection == nil || len(environment.TableSection) == 0 || len(environment.GlobalSection) != 5 {
		return nil, nil, errors.New("央视 WASM 环境结构不完整")
	}
	return wabinbinary.EncodeModule(environment), functionImports, nil
}

func extractWASM(source []byte) ([]byte, error) {
	const base64Marker = "base64,"
	start := bytes.Index(source, []byte("var wasmBinaryFile="))
	if start < 0 {
		// CCTV's August 2026 worker minifies the embedded WASM URL to M.
		start = bytes.Index(source, []byte(`var M="data:application/octet-stream;base64,`))
		if start < 0 {
			return nil, errors.New("央视模块不包含已支持的内嵌 WASM，可能需要更新解密器")
		}
	}
	relative := bytes.Index(source[start:], []byte(base64Marker))
	if relative < 0 {
		return nil, errors.New("央视模块中没有内嵌 WASM")
	}
	start += relative + len(base64Marker)
	end := bytes.IndexByte(source[start:], '"')
	if end < 0 {
		return nil, errors.New("央视模块中的 WASM 数据不完整")
	}
	decoded, err := base64.StdEncoding.DecodeString(string(source[start : start+end]))
	if err != nil {
		return nil, fmt.Errorf("解码央视 WASM: %w", err)
	}
	if len(decoded) < 8 || !bytes.Equal(decoded[:4], []byte{'\x00', 'a', 's', 'm'}) {
		return nil, errors.New("央视模块中的 WASM 格式无效")
	}
	return decoded, nil
}

func extractLookupTable(source []byte, layout moduleLayout) ([]byte, error) {
	startMarker, callMarker, offsetMarker := "__ATPRERUN__.push(function(){", "HEAPU8.set(", ",eb"
	if layout.modern {
		startMarker, callMarker, offsetMarker = "La.push(function(){", "F.set(", ",N"
	}
	start := bytes.Index(source, []byte(startMarker))
	if start < 0 {
		return nil, errors.New("央视模块中没有解密查找表")
	}
	start += len(startMarker)
	table := make([]byte, 0, 1558792)
	position := start
	for {
		relative := bytes.Index(source[position:], []byte(callMarker))
		if relative < 0 {
			break
		}
		callStart := position + relative + len(callMarker)
		callEnd := matchingParenthesis(source, callStart)
		if callEnd < 0 {
			return nil, errors.New("央视模块中的解密查找表调用不完整")
		}
		body := source[callStart:callEnd]
		separator := bytes.LastIndex(body, []byte(offsetMarker))
		if separator < 0 {
			break
		}
		arraySource := strings.TrimSpace(string(body[:separator]))
		offsetSource := strings.TrimSpace(string(body[separator+len(offsetMarker):]))
		if len(arraySource) < 2 || arraySource[0] != '[' || arraySource[len(arraySource)-1] != ']' {
			return nil, errors.New("央视解密查找表不是数字数组")
		}
		offset := 0
		if offsetSource != "" {
			if offsetSource[0] != '+' {
				return nil, fmt.Errorf("央视解密查找表偏移无效: %s%s", offsetMarker, offsetSource)
			}
			parsed, err := strconv.ParseFloat(offsetSource[1:], 64)
			if err != nil {
				return nil, fmt.Errorf("解析央视解密查找表偏移: %w", err)
			}
			offset = int(parsed)
		}
		if offset != len(table) {
			return nil, fmt.Errorf("央视解密查找表偏移不连续: %d != %d", offset, len(table))
		}
		values := strings.Split(arraySource[1:len(arraySource)-1], ",")
		for i, raw := range values {
			value, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
			if err != nil || value < 0 || value > 255 || math.Trunc(value) != value {
				return nil, fmt.Errorf("央视解密查找表第 %d 项不是字节: %q", len(table)+i+1, raw)
			}
			table = append(table, byte(value))
		}
		position = callEnd + 1
	}
	if len(table) < 1024 || uint64(layout.lookupTableBase)+uint64(len(table)) > 16*1024*1024 {
		return nil, fmt.Errorf("央视解密查找表长度异常: %d", len(table))
	}
	return table, nil
}

func extractLookupRelocations(source []byte, tableLength int, modern bool) ([]uint32, error) {
	const startMarker = ";var A=[];A="
	endMarker := ";for(var e=0;e<A.length;e++)HEAPU32"
	if modern {
		endMarker = ";for(var B=0;B<A.length;B++)L[N+A[B]>>2]+=N"
	}
	start := bytes.Index(source, []byte(startMarker))
	if start < 0 {
		return nil, errors.New("央视模块中没有解密重定位表")
	}
	start += len(startMarker)
	end := bytes.Index(source[start:], []byte(endMarker))
	if end < 0 {
		return nil, errors.New("央视模块中的解密重定位表不完整")
	}
	body := source[start : start+end]
	relocations := make([]uint32, 0, 24000)
	position := 0
	for {
		open := bytes.IndexByte(body[position:], '[')
		if open < 0 {
			break
		}
		open += position
		close := bytes.IndexByte(body[open+1:], ']')
		if close < 0 {
			return nil, errors.New("央视解密重定位数组不完整")
		}
		close += open + 1
		for _, raw := range strings.Split(string(body[open+1:close]), ",") {
			value, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
			if err != nil || value < 0 || value > math.MaxUint32 || math.Trunc(value) != value {
				return nil, fmt.Errorf("解析央视解密重定位项 %d: %q", len(relocations)+1, raw)
			}
			offset := uint32(value)
			if offset%4 != 0 || uint64(offset)+4 > uint64(tableLength) {
				return nil, fmt.Errorf("央视解密重定位偏移异常: %d", offset)
			}
			relocations = append(relocations, offset)
		}
		position = close + 1
	}
	if len(relocations) == 0 {
		return nil, errors.New("央视解密重定位表为空")
	}
	return relocations, nil
}

func matchingParenthesis(source []byte, start int) int {
	depth := 1
	var quote byte
	escaped := false
	for index := start; index < len(source); index++ {
		current := source[index]
		if quote != 0 {
			if escaped {
				escaped = false
			} else if current == '\\' {
				escaped = true
			} else if current == quote {
				quote = 0
			}
			continue
		}
		switch current {
		case '\'', '"', '`':
			quote = current
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return index
			}
		}
	}
	return -1
}

func (e *Engine) functionName(legacy, modern string) string {
	if e.layout.modern {
		return modern
	}
	return legacy
}

func (e *Engine) instantiateHostModule(
	ctx context.Context,
	module *wabinwasm.Module,
	imports []*wabinwasm.Import,
) error {
	builder := e.runtime.NewHostModuleBuilder("goenv")
	for _, imp := range imports {
		functionType := module.TypeSection[imp.DescFunc]
		params := make([]api.ValueType, len(functionType.Params))
		results := make([]api.ValueType, len(functionType.Results))
		for i, valueType := range functionType.Params {
			params[i] = api.ValueType(valueType)
		}
		for i, valueType := range functionType.Results {
			results[i] = api.ValueType(valueType)
		}
		builder.NewFunctionBuilder().
			WithGoModuleFunction(e.hostFunction(imp.Name), params, results).
			Export(imp.Name)
	}
	if _, err := builder.Instantiate(ctx); err != nil {
		return fmt.Errorf("初始化央视 WASM 宿主: %w", err)
	}
	return nil
}

func sessionFromContext(ctx context.Context) *hostSession {
	session, _ := ctx.Value(sessionContextKey{}).(*hostSession)
	return session
}

func (e *Engine) hostFunction(name string) api.GoModuleFunction {
	if e.layout.modern {
		// Emscripten's new minifier assigned different export/import names.
		switch name {
		case "T":
			name = "g" // getTempRet0
		case "g":
			name = "f" // emscripten_date_now
		case "f":
			name = "p" // gettimeofday
		case "W":
			name = "V" // cxa_uncaught_exceptions
		case "U":
			name = "T" // filesystem syscall
		case "m":
			name = "n" // abortOnCannotGrowMemory
		case "n", "ba":
			name = "o" // trap/abort
		}
	}
	return api.GoModuleFunc(func(ctx context.Context, module api.Module, stack []uint64) {
		memory := module.Memory()
		session := sessionFromContext(ctx)
		switch name {
		case "e": // setTempRet0
			if session != nil {
				session.tempRet0 = uint32(stack[0])
			}
		case "g": // getTempRet0
			if session != nil {
				stack[0] = uint64(session.tempRet0)
			}
		case "f": // emscripten_date_now
			stack[0] = math.Float64bits(float64(time.Now().UnixMilli()))
		case "p": // gettimeofday
			now := time.Now()
			pointer := uint32(stack[0])
			memory.WriteUint32Le(pointer, uint32(now.Unix()))
			memory.WriteUint32Le(pointer+4, uint32(now.Nanosecond()/1000))
			stack[0] = 0
		case "s": // emscripten_memcpy_big
			destination, source, size := uint32(stack[0]), uint32(stack[1]), uint32(stack[2])
			data, ok := memory.Read(source, size)
			if !ok || !memory.Write(destination, append([]byte(nil), data...)) {
				panic("央视 WASM 内存复制越界")
			}
			// The generated JavaScript helper has no return statement; WebAssembly
			// receives JavaScript's undefined coerced to i32 zero.
			stack[0] = 0
		case "r": // emscripten_resize_heap
			requested := uint32(stack[0])
			current := memory.Size()
			if requested <= current {
				stack[0] = 1
				return
			}
			pages := (requested - current + 65535) / 65536
			_, ok := memory.Grow(pages)
			if ok {
				stack[0] = 1
			} else {
				stack[0] = 0
			}
		case "u": // emscripten_is_main_browser_thread
			stack[0] = 0
		case "v": // emscripten_get_heap_size
			stack[0] = uint64(memory.Size())
		case "y": // emscripten_asm_const_ii
			stack[0] = uint64(e.evaluateEnvironment(ctx, module, uint32(stack[1])))
		case "A": // __emval_take_value
			stack[0] = uint64(e.emvalTakeValue(ctx, module, session, uint32(stack[0]), uint32(stack[1])))
		case "B": // __emval_run_destructors
			e.emvalRunDestructors(ctx, module, session, uint32(stack[0]))
		case "C": // __emval_new_cstring
			stack[0] = uint64(session.registerValue(readCString(memory, uint32(stack[0]))))
		case "D": // __emval_get_property
			object := session.value(uint32(stack[0]))
			property := fmt.Sprint(session.value(uint32(stack[1])))
			value := any(undefinedValue{})
			if properties, ok := object.(emvalObject); ok {
				if found, exists := properties[property]; exists {
					value = found
				}
			}
			stack[0] = uint64(session.registerValue(value))
		case "E": // __emval_get_global
			stack[0] = uint64(session.registerValue(session.globalValue(readCString(memory, uint32(stack[0])))))
		case "F": // __emval_decref
			session.decref(uint32(stack[0]))
		case "G": // __emval_as
			stack[0] = e.emvalAs(ctx, module, session, uint32(stack[0]), uint32(stack[1]), uint32(stack[2]))
		case "J": // __embind_register_void
			session.types[uint32(stack[0])] = emvalType{kind: "void"}
		case "K": // __embind_register_std_wstring
			session.types[uint32(stack[0])] = emvalType{kind: "wstring", size: uint32(stack[1])}
		case "L": // __embind_register_std_string
			session.types[uint32(stack[0])] = emvalType{kind: "string"}
		case "N": // __embind_register_integer
			session.types[uint32(stack[0])] = emvalType{kind: "integer", size: uint32(stack[2])}
		case "O": // __embind_register_float
			session.types[uint32(stack[0])] = emvalType{kind: "float", size: uint32(stack[2])}
		case "P": // __embind_register_emval
			session.types[uint32(stack[0])] = emvalType{kind: "emval", size: 4}
		case "Q": // __embind_register_bool
			session.types[uint32(stack[0])] = emvalType{kind: "bool", size: uint32(stack[2])}
		case "w": // emscripten_get_callstack
			// CCTV's VOD VM requests EM_LOG_JS_STACK and consumes both the
			// returned length and the Emscripten-formatted stack text. Reproduce
			// the worker call chain that the official browser loader exposes.
			callStack := "    at asmLibraryArg.<computed> (http://tv.cctv.com:3010/cctv.worker.js:2:2334601)\n" +
				"    at wasm://wasm/000554ba:wasm-function[50]:0x1d4e\n" +
				"    at wasm://wasm/000554ba:wasm-function[50]:0x19c0\n" +
				"    at wasm://wasm/000554ba:wasm-function[50]:0x19c0\n" +
				"    at wasm://wasm/000554ba:wasm-function[103]:0x769a\n" +
				"    at Module._CNTV_jsdecVOD8 (http://tv.cctv.com:3010/cctv.worker.js:2:2337065)\n" +
				"    at Object.MpegAudio (http://tv.cctv.com:3010/official-transmux-worker.js:2:48557)"
			pointer, capacity := uint32(stack[1]), uint32(stack[2])
			if pointer == 0 || capacity == 0 {
				stack[0] = uint64(len(callStack) + 1)
				break
			}
			written := min(len(callStack), int(capacity)-1)
			if !memory.Write(pointer, append([]byte(callStack[:written]), 0)) {
				panic("央视 WASM 调用栈写入越界")
			}
			stack[0] = uint64(written + 1)
		case "q": // emscripten_start_fetch
			// Emscripten returns the fetch structure immediately. The CCTV VOD
			// module completes this auxiliary request on the worker event loop.
			// Queue it so Session can reproduce that ordering after Init returns.
			if session != nil {
				session.pendingFetches = append(session.pendingFetches, uint32(stack[0]))
			}
		case "V": // cxa_uncaught_exceptions
			stack[0] = 0
		case "R", "S", "T": // unused filesystem syscalls
			stack[0] = uint64(uint32(math.MaxUint32))
		case "n": // abortOnCannotGrowMemory
			panic("央视 WASM 内存不足")
		case "o", "z", "$", "h":
			panic(fmt.Sprintf("央视 WASM 调用了终止函数 %q", name))
		default:
			// Embind, fetch and function-pointer helpers are not used by the VOD
			// C API. Zero is the browser glue's neutral return value.
			if len(stack) > 0 {
				stack[0] = 0
			}
		}
	})
}

func (s *hostSession) registerValue(value any) uint32 {
	switch typed := value.(type) {
	case undefinedValue:
		return 1
	case nil:
		return 2
	case bool:
		if typed {
			return 3
		}
		return 4
	}
	if s.nextHandle < 5 {
		s.nextHandle = 5
	}
	var handle uint32
	if count := len(s.freeHandles); count > 0 {
		handle = s.freeHandles[count-1]
		s.freeHandles = s.freeHandles[:count-1]
	} else {
		handle = s.nextHandle
		s.nextHandle++
	}
	s.handles[handle] = value
	s.handleRefs[handle] = 1
	return handle
}

func (s *hostSession) value(handle uint32) any {
	switch handle {
	case 1:
		return undefinedValue{}
	case 2:
		return nil
	case 3:
		return true
	case 4:
		return false
	default:
		if value, ok := s.handles[handle]; ok {
			return value
		}
		return undefinedValue{}
	}
}

func (s *hostSession) decref(handle uint32) {
	if handle <= 4 {
		return
	}
	refs := s.handleRefs[handle]
	if refs > 1 {
		s.handleRefs[handle] = refs - 1
		return
	}
	if refs == 1 {
		delete(s.handles, handle)
		delete(s.handleRefs, handle)
		s.freeHandles = append(s.freeHandles, handle)
	}
}

func (s *hostSession) location() emvalObject {
	href := s.locationURL
	if href == "" {
		href = "https://tv.cctv.com/official-transmux-worker.js"
	}
	host := "tv.cctv.com"
	protocol := "https:"
	origin := strings.TrimRight(href, "/")
	if parsed, err := url.Parse(href); err == nil {
		if parsed.Host != "" {
			host = parsed.Host
		}
		if parsed.Scheme != "" {
			protocol = parsed.Scheme + ":"
		}
		origin = parsed.Scheme + "://" + parsed.Host
	}
	return emvalObject{"host": host, "href": href, "protocol": protocol, "origin": origin}
}

func (s *hostSession) globalValue(name string) any {
	switch name {
	case "location":
		return s.location()
	case "self", "globalThis":
		return emvalObject{"location": s.location()}
	case "window":
		return undefinedValue{}
	default:
		return undefinedValue{}
	}
}

func (e *Engine) emvalTakeValue(ctx context.Context, module api.Module, session *hostSession, typeID, pointer uint32) uint32 {
	typeInfo := session.types[typeID]
	value := any(undefinedValue{})
	switch typeInfo.kind {
	case "string":
		valuePointer, ok := module.Memory().ReadUint32Le(pointer)
		if ok {
			value = readNativeString(module.Memory(), valuePointer)
			if free := module.ExportedFunction(e.functionName("Ba", "Da")); free != nil && valuePointer != 0 {
				_, _ = free.Call(ctx, uint64(valuePointer))
			}
		}
	case "emval":
		if handle, ok := module.Memory().ReadUint32Le(pointer); ok {
			value = session.value(handle)
			session.decref(handle)
		}
	case "integer", "bool":
		value = readNativeInteger(module.Memory(), pointer, typeInfo.size)
	case "float":
		if typeInfo.size == 4 {
			if bits, ok := module.Memory().ReadUint32Le(pointer); ok {
				value = float64(math.Float32frombits(bits))
			}
		} else if bits, ok := module.Memory().ReadUint64Le(pointer); ok {
			value = math.Float64frombits(bits)
		}
	}
	return session.registerValue(value)
}

func (e *Engine) emvalAs(ctx context.Context, module api.Module, session *hostSession, handle, typeID, destructorPointer uint32) uint64 {
	typeInfo := session.types[typeID]
	value := session.value(handle)
	switch typeInfo.kind {
	case "string":
		textValue := fmt.Sprint(value)
		allocationSize := uint64(len(textValue) + 5)
		results, err := module.ExportedFunction("Fa").Call(ctx, allocationSize)
		if err != nil || len(results) == 0 || results[0] == 0 {
			panic(fmt.Sprintf("央视 WASM 分配 emval 字符串失败: %v", err))
		}
		pointer := uint32(results[0])
		module.Memory().WriteUint32Le(pointer, uint32(len(textValue)))
		if !module.Memory().Write(pointer+4, append([]byte(textValue), 0)) {
			panic("央视 WASM 写入 emval 字符串越界")
		}
		destructorHandle := session.registerValue([]uint32{pointer})
		module.Memory().WriteUint32Le(destructorPointer, destructorHandle)
		return math.Float64bits(float64(pointer))
	case "float":
		module.Memory().WriteUint32Le(destructorPointer, session.registerValue([]uint32{}))
		number, _ := value.(float64)
		return math.Float64bits(number)
	default:
		module.Memory().WriteUint32Le(destructorPointer, session.registerValue([]uint32{}))
		return 0
	}
}

func (e *Engine) emvalRunDestructors(ctx context.Context, module api.Module, session *hostSession, handle uint32) {
	if pointers, ok := session.value(handle).([]uint32); ok {
		for index := len(pointers) - 1; index >= 0; index-- {
			if free := module.ExportedFunction(e.functionName("Ba", "Da")); free != nil && pointers[index] != 0 {
				_, _ = free.Call(ctx, uint64(pointers[index]))
			}
		}
	}
	session.decref(handle)
}

func readNativeString(memory api.Memory, pointer uint32) string {
	length, ok := memory.ReadUint32Le(pointer)
	if !ok || length > 16*1024*1024 {
		return ""
	}
	value, ok := memory.Read(pointer+4, length)
	if !ok {
		return ""
	}
	return string(value)
}

func readNativeInteger(memory api.Memory, pointer, size uint32) uint64 {
	switch size {
	case 1:
		value, _ := memory.ReadByte(pointer)
		return uint64(value)
	case 2:
		value, _ := memory.ReadUint16Le(pointer)
		return uint64(value)
	default:
		value, _ := memory.ReadUint32Le(pointer)
		return uint64(value)
	}
}

func (e *Engine) evaluateEnvironment(ctx context.Context, module api.Module, namePointer uint32) uint32 {
	name := readCString(module.Memory(), namePointer)
	session := sessionFromContext(ctx)
	locationURL := "https://tv.cctv.com/official-transmux-worker.js"
	if session != nil && session.locationURL != "" {
		locationURL = session.locationURL
	}
	parsedLocation, _ := url.Parse(locationURL)
	value := ""
	switch name {
	case "self.location.host":
		value = parsedLocation.Host
	case "self.location.href":
		value = locationURL
	case "self.location.protocol":
		value = parsedLocation.Scheme + ":"
	case "window":
		value = "[object Window]"
	case "electronAPI":
		value = "undefined"
	default:
		value = ""
	}
	malloc := module.ExportedFunction(e.functionName("Fa", "Ha"))
	results, err := malloc.Call(ctx, uint64(len(value)+1))
	if err != nil || len(results) == 0 {
		panic(fmt.Sprintf("央视 WASM 分配环境字符串失败: %v", err))
	}
	pointer := uint32(results[0])
	if !module.Memory().Write(pointer, append([]byte(value), 0)) {
		panic("央视 WASM 写入环境字符串越界")
	}
	return pointer
}

func readCString(memory api.Memory, pointer uint32) string {
	buffer := make([]byte, 0, 64)
	for offset := uint32(0); offset < 4096; offset++ {
		value, ok := memory.ReadByte(pointer + offset)
		if !ok || value == 0 {
			break
		}
		buffer = append(buffer, value)
	}
	return string(buffer)
}

// Close releases the compiled decryption runtime.
func (e *Engine) Close(ctx context.Context) error {
	return e.runtime.Close(ctx)
}

func writeUint32(memory api.Memory, offset, value uint32) error {
	buffer := make([]byte, 4)
	binary.LittleEndian.PutUint32(buffer, value)
	if !memory.Write(offset, buffer) {
		return errors.New("WASM 内存写入越界")
	}
	return nil
}
