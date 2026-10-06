package core

import (
	"context"
	_ "embed"
	"encoding/hex"
	"errors"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

//go:embed ysp_web/keygen.wasm
var yspWebKeygenWASM []byte

//go:embed ysp_web/ticket.wasm
var yspWebTicketWASM []byte

type yspWebKeygenContextKey struct{}

type yspWebKeygenState struct {
	values map[string]string
	heap   []any
}

type yspWebSigner struct {
	runtime wazero.Runtime
	keygen  wazero.CompiledModule
	ticket  wazero.CompiledModule
}

func newYSPWebSigner(ctx context.Context) (*yspWebSigner, error) {
	runtime := wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfigInterpreter().WithMemoryLimitPages(256).WithCloseOnContextDone(true))
	signer := &yspWebSigner{runtime: runtime}
	var err error
	signer.keygen, err = runtime.CompileModule(ctx, yspWebKeygenWASM)
	if err == nil {
		signer.ticket, err = runtime.CompileModule(ctx, yspWebTicketWASM)
	}
	if err == nil {
		_, err = runtime.NewHostModuleBuilder("wbg").
			NewFunctionBuilder().WithFunc(func(ctx context.Context, module api.Module, pointer, length uint32) uint32 {
			state := ctx.Value(yspWebKeygenContextKey{}).(*yspWebKeygenState)
			key, ok := module.Memory().Read(pointer, length)
			if !ok {
				panic("invalid Web signing input")
			}
			state.heap = append(state.heap, state.values[string(key)])
			return uint32(len(state.heap) - 1)
		}).Export("__wbg_get_9c1840f7ecd81363").
			NewFunctionBuilder().WithFunc(func(ctx context.Context, module api.Module, output, index uint32) {
			state := ctx.Value(yspWebKeygenContextKey{}).(*yspWebKeygenState)
			if int(index) >= len(state.heap) {
				panic("invalid Web signing reference")
			}
			pointer, length := uint32(0), uint32(0)
			if value, ok := state.heap[index].(string); ok {
				allocated, err := module.ExportedFunction("__wbindgen_malloc").Call(ctx, uint64(len(value)), 1)
				if err != nil || len(allocated) != 1 {
					panic("Web signing allocation failed")
				}
				pointer, length = uint32(allocated[0]), uint32(len(value))
				if !module.Memory().Write(pointer, []byte(value)) {
					panic("invalid Web signing memory")
				}
			}
			if !module.Memory().WriteUint32Le(output, pointer) || !module.Memory().WriteUint32Le(output+4, length) {
				panic("invalid Web signing memory")
			}
		}).Export("__wbindgen_string_get").
			NewFunctionBuilder().WithFunc(func(ctx context.Context, index uint32) {
			state := ctx.Value(yspWebKeygenContextKey{}).(*yspWebKeygenState)
			if index >= 132 && int(index) < len(state.heap) {
				state.heap[index] = nil
			}
		}).Export("__wbindgen_object_drop_ref").Instantiate(ctx)
	}
	if err == nil {
		host := runtime.NewHostModuleBuilder("a")
		for _, imported := range signer.ticket.ImportedFunctions() {
			_, name, _ := imported.Import()
			host.NewFunctionBuilder().WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, module api.Module, stack []uint64) {
				yspWebTicketHost(name, module, stack)
			}), imported.ParamTypes(), imported.ResultTypes()).Export(name)
		}
		_, err = host.Instantiate(ctx)
	}
	if err != nil {
		runtime.Close(context.Background())
		return nil, errors.New("央视频 Web 签名模块初始化失败")
	}
	return signer, nil
}

func yspWebTicketHost(name string, module api.Module, stack []uint64) {
	memory := module.Memory()
	now := time.Now()
	result := uint64(0)
	switch name {
	case "d":
		result = uint64(uint32(now.Unix()))
		if stack[0] != 0 {
			memory.WriteUint32Le(uint32(stack[0]), uint32(result))
		}
	case "e":
		result = 42
	case "i":
		memory.WriteUint32Le(uint32(stack[0]), uint32(now.Unix()))
		memory.WriteUint32Le(uint32(stack[0])+4, uint32(now.Nanosecond()/1_000_000*1000))
	case "j":
		memory.WriteUint32Le(uint32(stack[1]), uint32(now.Unix()))
		memory.WriteUint32Le(uint32(stack[1])+4, uint32(now.Nanosecond()/1_000_000*1_000_000))
	case "o", "q":
		result = 0xffffffff
	case "s":
		memory.WriteUint32Le(uint32(stack[4]), 0)
		memory.WriteUint32Le(uint32(stack[4])+4, 0)
	case "t":
		data, ok := memory.Read(uint32(stack[1]), uint32(stack[2]))
		if !ok || !memory.Write(uint32(stack[0]), data) {
			panic("invalid Web ticket memory")
		}
		result = stack[0]
	case "v":
		memory.WriteByte(uint32(stack[1]), 4)
	case "x":
		memory.WriteUint32Le(uint32(stack[0]), 0)
		memory.WriteUint32Le(uint32(stack[1]), 0)
	case "O":
		result = 1
	}
	if len(stack) > 0 {
		stack[0] = result
	}
}

func (signer *yspWebSigner) key(ctx context.Context, values map[string]string, function string) (string, error) {
	state := &yspWebKeygenState{values: values, heap: make([]any, 132)}
	state.heap[129], state.heap[130], state.heap[131] = "null", "true", "false"
	ctx = context.WithValue(ctx, yspWebKeygenContextKey{}, state)
	module, err := signer.runtime.InstantiateModule(ctx, signer.keygen, wazero.NewModuleConfig().WithName("").WithStartFunctions())
	if err != nil {
		return "", err
	}
	defer module.Close(context.Background())
	stack := module.ExportedFunction("__wbindgen_add_to_stack_pointer")
	allocated, err := stack.Call(ctx, 0xfffffff0)
	if err != nil {
		return "", err
	}
	output := uint32(allocated[0])
	if _, err = module.ExportedFunction(function).Call(ctx, uint64(output)); err != nil {
		return "", err
	}
	pointer, first := module.Memory().ReadUint32Le(output)
	length, second := module.Memory().ReadUint32Le(output + 4)
	if !first || !second || length > 4096 {
		return "", errors.New("央视频 Web 签名结果无效")
	}
	data, ok := module.Memory().Read(pointer, length)
	if !ok {
		return "", errors.New("央视频 Web 签名结果无效")
	}
	value := string(data)
	if _, err = stack.Call(ctx, 16); err != nil {
		return "", err
	}
	return value, nil
}

func (signer *yspWebSigner) playerTicket(ctx context.Context, pid, timestamp, sid, guid, appID, version string) (string, error) {
	module, err := signer.runtime.InstantiateModule(ctx, signer.ticket, wazero.NewModuleConfig().WithName("").WithStartFunctions())
	if err != nil {
		return "", err
	}
	defer module.Close(context.Background())
	if _, err = module.ExportedFunction("P").Call(ctx); err != nil {
		return "", err
	}
	args := []uint64{}
	for _, value := range []string{pid, timestamp, sid, guid, appID, version} {
		allocated, err := module.ExportedFunction("R").Call(ctx, uint64(len(value)+1))
		if err != nil {
			return "", err
		}
		if !module.Memory().Write(uint32(allocated[0]), append([]byte(value), 0)) {
			return "", errors.New("央视频 Web 凭据输入无效")
		}
		args = append(args, allocated[0])
	}
	length := uint32(len(pid) + len(timestamp) + len(guid) + len(appID) + 14)
	allocated, err := module.ExportedFunction("R").Call(ctx, uint64(length))
	if err != nil {
		return "", err
	}
	args = append(args, allocated[0])
	if _, err = module.ExportedFunction("T").Call(ctx, args...); err != nil {
		return "", err
	}
	data, ok := module.Memory().Read(uint32(allocated[0]), length)
	if !ok {
		return "", errors.New("央视频 Web 凭据结果无效")
	}
	return hex.EncodeToString(data), nil
}
