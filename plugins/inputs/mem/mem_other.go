//go:build !linux

package mem

type noopExtendedMemoryStats struct{}

func newExtendedMemoryStats() extendedMemoryStats {
	return &noopExtendedMemoryStats{}
}

// addFields is a no-op on non-Linux platforms as extended VM stats are not available.
func (n *noopExtendedMemoryStats) addFields(fields map[string]interface{}) error {
	return nil
}
