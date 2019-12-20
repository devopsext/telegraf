package procstat_boost

import (
	"errors"
	"fmt"
	"github.com/dlclark/regexp2"
	"github.com/shirou/gopsutil/process"
	"io/ioutil"
	"log"
	"strconv"
	"strings"
)

//NativeFinder uses gopsutil to find processes
type NativeFinder struct {
	regexp          *regexp2.Regexp
	regexpFullMatch bool
}

//NewNativeFinder ...
func NewNativeFinder(procstatBoost *ProcstatBoost) (PIDFinder, error) {
	var regexString = ""
	//Checking if regex is valid
	if procstatBoost.Exe != "" && procstatBoost.Pattern != "" {
		return nil, errors.New("settings ambiguity: 'pattern' & 'exe' are mutually exclusive")
	}

	nativeFinder := NativeFinder{}
	if procstatBoost.Exe != "" {
		regexString = procstatBoost.Exe
	} else if procstatBoost.Pattern != "" {
		regexString = procstatBoost.Pattern
	}

	nativeFinder.regexp = regexp2.MustCompile(regexString, regexp2.None)

	if regexString == ".*" {
		nativeFinder.regexpFullMatch = true
		log.Printf("D! [inputs.procstat_boost] native finder regex full match = true")
	}

	return &nativeFinder, nil
}

//Uid will return all pids for the given user
func (nf *NativeFinder) Uid(user string) ([]PID, error) {
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
func (nf *NativeFinder) PidFile(path string) ([]PID, error) {
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

func (nf *NativeFinder) XtraConfig(rawArgs []string) ([]PID, error) {
	var pids []PID
	return pids, nil
}
