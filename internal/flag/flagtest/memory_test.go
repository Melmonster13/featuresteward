package flagtest

import (
	"testing"

	"github.com/Melmonster13/featuresteward/internal/flag"
)

func TestMemoryContract(t *testing.T) {
	RunContract(t, func(*testing.T) flag.Store { return NewMemory() })
}
