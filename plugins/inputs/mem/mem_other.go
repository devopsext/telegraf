//go:build !linux

package mem

type noopExtendedMemoryStats struct{}

func newExtendedMemoryStats() extendedMemoryStats {
	return &noopExtendedMemoryStats{}
}

// getFields returns nil on non-Linux platforms as extended VM stats are not available.
func (n *noopExtendedMemoryStats) getFields() (map[string]interface{}, error) {
	return nil, nil
}
