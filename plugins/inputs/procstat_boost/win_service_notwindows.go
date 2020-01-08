// +build !windows

package procstat_boost

import (
	"fmt"
)

func queryPidWithWinServiceName(winServiceName string) (uint32, error) {
	return 0, fmt.Errorf("os not support win_service option")
}
