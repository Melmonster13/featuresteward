package authtest

import (
	"testing"

	"github.com/Melmonster13/featuresteward/internal/auth"
)

func TestMemoryContract(t *testing.T) {
	RunContract(t, func(*testing.T) auth.Store { return NewMemory() })
}
