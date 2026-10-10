package svcproxy

import (
	"fmt"

	"google.golang.org/grpc/mem"
)

// frame is one gRPC message carried as opaque bytes.
type frame struct{ payload []byte }

// rawCodec is an encoding.CodecV2 that moves message bytes through the proxy
// undecoded, so the proxy needs no knowledge of any service's messages. Its
// name is "proto" so the content-subtype on both hops stays
// application/grpc+proto, which every backend expects.
type rawCodec struct{}

func (rawCodec) Name() string { return "proto" }

func (rawCodec) Marshal(v any) (mem.BufferSlice, error) {
	f, ok := v.(*frame)
	if !ok {
		return nil, fmt.Errorf("svcproxy: rawCodec cannot marshal %T", v)
	}
	return mem.BufferSlice{mem.SliceBuffer(f.payload)}, nil
}

func (rawCodec) Unmarshal(data mem.BufferSlice, v any) error {
	f, ok := v.(*frame)
	if !ok {
		return fmt.Errorf("svcproxy: rawCodec cannot unmarshal into %T", v)
	}
	// data is freed once Unmarshal returns, so the payload must be copied.
	f.payload = data.Materialize()
	return nil
}
