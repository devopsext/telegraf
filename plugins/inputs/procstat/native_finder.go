package procstat

import (
	"fmt"
	"github.com/dlclark/regexp2"
	"github.com/shirou/gopsutil/process"
	"io/ioutil"
	"log"
	"strconv"
	"strings"
	"errors"
)

//NativeFinder uses gopsutil to find processes
type NativeFinder struct {
	regexPattern *regexp2.Regexp
	regexExe *regexp2.Regexp
	regexExeFullMatch bool
	regexPatternFullMatch bool
}

//NewNativeFinder ...
func NewNativeFinder(procstat *Procstat) (PIDFinder, error) {
	var err error
	//Checking if regex is valid
	if procstat.Exe != "" && procstat.Pattern != "" {
		return nil, errors.New("settings ambiguity: 'pattern' & 'exe' are mutually exclusive")
	}

	nativeFinder := NativeFinder{}

	if procstat.Exe != "" {
		nativeFinder.regexPattern = regexp2.MustCompile(procstat.Exe,regexp2.None)

		if err != nil {
			return nil, err
		}

		if procstat.Exe == ".*" {
			nativeFinder.regexExeFullMatch = true
			log.Printf("D! [inputs.procstat] native finder regex exe full match = true")
		}
	}


	if procstat.Pattern != "" {
		nativeFinder.regexPattern = regexp2.MustCompile(procstat.Pattern,regexp2.None)

		if err != nil {
			return nil, err
		}

		if procstat.Pattern == ".*" {
			nativeFinder.regexPatternFullMatch = true
			log.Printf("D! [inputs.procstat] native finder regex pattern full match = true")
		}
	}


	return &nativeFinder, nil
}

//Uid will return all pids for the given user
func (pg *NativeFinder) Uid(user string) ([]PID, error) {
	var dst []PID

	procs, err := process.Processes()
	if err != nil {
		return dst, err
	}
	for _, p := range procs {
		username, err := p.Username()
		if err != nil {
			//skip, this can happen if we don't have permissions or
			//the pid no longer exists
			continue
		}
		if username == user {
			dst = append(dst, PID(p.Pid))
		}
	}
	return dst, nil
}

//PidFile returns the pid from the pid file given.
func (pg *NativeFinder) PidFile(path string) ([]PID, error) {
	var pids []PID
	pidString, err := ioutil.ReadFile(path)
	if err != nil {
		return pids, fmt.Errorf("Failed to read pidfile '%s'. Error: '%s'",
			path, err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidString)))
	if err != nil {
		return pids, err
	}
	pids = append(pids, PID(pid))
	return pids, nil

}

//Added by IP
func (pg *NativeFinder) AddData(rawArgs []string) ([]PID, error) {
	var pids []PID
	return pids, nil
}