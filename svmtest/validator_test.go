package svmtest

import (
	"fmt"
	"testing"
)

func TestGenesisVisibilityRetryRecognizesOnlyExactPreflightError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "wrapped exact error",
			err: fmt.Errorf("submit outcome unknown for locally signed transaction: %w", &RPCError{
				Code: -32002, Message: "Transaction simulation failed: Unsupported program id",
			}),
			want: true,
		},
		{name: "wrong code", err: &RPCError{Code: -32603, Message: "Unsupported program id"}},
		{name: "wrong message", err: &RPCError{Code: -32002, Message: "Node is unhealthy"}},
		{name: "transport ambiguity", err: fmt.Errorf("connection reset")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := isGenesisProgramVisibilityDelay(test.err); got != test.want {
				t.Fatalf("isGenesisProgramVisibilityDelay(%v) = %v, want %v", test.err, got, test.want)
			}
		})
	}
}
