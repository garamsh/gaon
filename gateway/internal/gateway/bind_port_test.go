package gateway_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/garamsh/gaon/gateway/internal/gateway"
)

func TestBindPort_APortBelow1024IsShiftedAndAnyOtherIsKept(t *testing.T) {
	tests := []struct {
		port, want int32
	}{
		{80, 10080},
		{443, 10443},
		{1, 10001},
		{1023, 11023},
		{1024, 1024},
		{8080, 8080},
		{65535, 65535},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, gateway.BindPort(tt.port), "port %d", tt.port)
	}
}
