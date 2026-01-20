package mem

import (
	"github.com/shirou/gopsutil/v4/mem"
)

type linuxExtendedMemoryStats struct{}

func newExtendedMemoryStats() extendedMemoryStats {
	return &linuxExtendedMemoryStats{}
}

// addFields adds extended virtual memory statistics from /proc/meminfo to the fields map.
func (l *linuxExtendedMemoryStats) addFields(fields map[string]interface{}) error {
	exVM, err := mem.NewExLinux().VirtualMemory()
	if err != nil {
		return err
	}
	fields["active_file"] = exVM.ActiveFile
	fields["inactive_file"] = exVM.InactiveFile
	fields["active_anon"] = exVM.ActiveAnon
	fields["inactive_anon"] = exVM.InactiveAnon
	fields["unevictable"] = exVM.Unevictable
	fields["percpu"] = exVM.Percpu
	return nil
}
