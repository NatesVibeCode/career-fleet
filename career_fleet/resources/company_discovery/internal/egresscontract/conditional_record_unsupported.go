//go:build !darwin && !linux

package egresscontract

import (
	"context"
	"fmt"
)

type NativeConditionalRecord struct{}

func (NativeConditionalRecord) Name() string { return "native.conditional_record.v2" }
func (NativeConditionalRecord) Supports(Policy) error {
	return fmt.Errorf("conditional records require supported descriptor-contained atomic publication")
}
func (a NativeConditionalRecord) Start(context.Context, Invocation) (InvocationHandle, error) {
	return nil, a.Supports(Policy{})
}
