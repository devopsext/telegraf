// +build !windows

package procstat_boost

import (
	gops "github.com/mitchellh/go-ps"
	"github.com/shirou/gopsutil/process"
	"log"
)

//Exe matches on the process name
func (nf *NativeFinder) Exe(pattern string) ([]PID, error) {
	var pids []PID

	procs, err := gops.Processes() //gops
	//procs, err := process.Processes()
	if err != nil {
		return pids, err
	}

	for _, p := range procs {
		name := p.Executable()
		//name, err := p.Exe()

		//if err != nil {
		//skip, this can be caused by the pid no longer existing
		//or you having no permissions to access it
		//	continue
		//}

		if !nf.regexpFullMatch {
			matched, err := nf.regexp.MatchString(name)
			if err != nil {
				//log.Printf("E! [inputs.procstat_boost] Can't check regex match of executable '%s', skipping this process (PID - '%d'). Reason: %v\n", name, p.Pid(), err)
				log.Printf("E! [inputs.procstat_boost] Can't check regex match of executable '%s', skipping this process (PID - '%d'). Reason: %v\n", name, p.Pid(), err)
				continue
			}

			if matched {
				pids = append(pids, PID(p.Pid()))
				//pids = append(pids, PID(p.Pid))
			}
		} else {
			pids = append(pids, PID(p.Pid()))
			//pids = append(pids, PID(p.Pid))
		}

	}
	return pids, err
}

//Pattern matches on the command line when the process was executed
func (nf *NativeFinder) Pattern(pattern string) ([]PID, error) {
	var pids []PID

	procs, err := process.Processes() //gopsutils!, uneffective!
	if err != nil {
		return pids, err
	}
	for _, p := range procs {
		cmd, err := p.Cmdline()
		if err != nil {
			//skip, this can be caused by the pid no longer existing
			//or you having no permissions to access it
			continue
		}

		if !nf.regexpFullMatch {
			matched, err := nf.regexp.MatchString(cmd)
			if err != nil {
				log.Printf("E! [inputs.procstat_boost] Can't check regex match of cmdline '%s', skipping this process (PID - '%d'). Reason: %v\n", cmd, p.Pid, err)
				continue
			}

			if matched {
				pids = append(pids, PID(p.Pid))
			}
		} else {
			pids = append(pids, PID(p.Pid))
		}
	}
	return pids, err
}
