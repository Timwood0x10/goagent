// Package ares_bootstrap — Memory provider.
package ares_bootstrap

import (
	"fmt"

	ares_memory "github.com/Timwood0x10/ares/internal/runtime/memory"
)

// ProvideMemory creates a MemoryManager. If cfg is nil, DefaultMemoryConfig() is used.
func ProvideMemory(cfg *ares_memory.MemoryConfig) (ares_memory.MemoryManager, error) {
	if cfg == nil {
		cfg = ares_memory.DefaultMemoryConfig()
	}
	mem, err := ares_memory.NewMemoryManager(cfg)
	if err != nil {
		return nil, fmt.Errorf("bootstrap: memory manager: %w", err)
	}
	return mem, nil
}
