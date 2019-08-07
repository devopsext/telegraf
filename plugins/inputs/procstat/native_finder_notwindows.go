// +build !windows

package procstat

import (
	"github.com/shirou/gopsutil/process"
	"log"
	gops "github.com/mitchellh/go-ps"
)

//Pattern matches on the process name
func (pg *NativeFinder) Pattern(pattern string) ([]PID, error) {
	var pids []PID
	/*
	regxPattern, err := regexp.Compile(pattern)
	if err != nil {
		return pids, err
	}*/

	procs2,err := gops.Processes()
	if err!=nil {
		return pids, err
	}
	/*
	procs, err := process.Processes()
	if err != nil {
		return pids, err
	}*/
	for _, p := range procs2 {
		//name, err := p.Exe()
		name := p.Executable()

		/*if err != nil {
			//skip, this can be caused by the pid no longer existing
			//or you having no permissions to access it
			continue
		}*/
		if ! pg.regexExeFullMatch {
			matched,err := pg.regexPattern.MatchString(name)
			if err != nil {
				log.Printf("E! [inputs.procstat] Can't check regex match of executable '%s', skipping this process (PID - '%d'). Reason: %v\n",name,p.Pid(),err)
				continue
			}

			//if regxPattern.MatchString(name) {
			if matched {
				//pids = append(pids, PID(p.Pid))
				pids = append(pids, PID(p.Pid()))
			}
		}else {
			pids = append(pids, PID(p.Pid()))
		}

	}
	return pids, err
}

//FullPattern matches on the command line when the process was executed
func (pg *NativeFinder) FullPattern(pattern string) ([]PID, error) {
	var pids []PID
	/*
	regxPattern, err := regexp.Compile(pattern)
	if err != nil {
		return pids, err
	}*/
	procs, err := process.Processes()
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

		if ! pg.regexPatternFullMatch {
			matched,err := pg.regexPattern.MatchString(cmd)
			if err != nil {
				log.Printf("E! [inputs.procstat] Can't check regex match of cmdline '%s', skipping this process (PID - '%d'). Reason: %v\n",cmd,p.Pid,err)
				continue
			}

			//if regxPattern.MatchString(cmd) {
			if matched {
				pids = append(pids, PID(p.Pid))
			}
		}else{
			pids = append(pids, PID(p.Pid))
		}
	}
	return pids, err
}
