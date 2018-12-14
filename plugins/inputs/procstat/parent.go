package procstat

import (
  "regexp"

	"github.com/shirou/gopsutil/process"
)

//ParentFinder uses gopsutil to find processes
type ParentFinder struct {
}

//NewParentFinder ...
func NewParentFinder() (PIDFinder, error) {
	return &ParentFinder{}, nil
}

func getChildPids(p *process.Process) ([]PID, error) {

	var pids []PID

	children, err := p.Children()
	if err !=nil {
		return pids, err
	}

	for _, child := range children {
		 pids = append(pids,PID(child.Pid))

		 childPids, err := getChildPids(child)
		 if err == nil {

			 for _, pid := range childPids {
				 pids = append(pids,PID(pid))
			 }
		 }
	}
	return pids, nil
}

func getByParent(cmdline string) ([]PID, error) {

	var pids []PID
	regxPattern, err := regexp.Compile(cmdline)
  if err != nil {
	  return pids, err
  }

  procs, err := process.Processes()
  if err != nil {
	  return pids, err
	}

  for _, proc := range procs {

		line, err := proc.Cmdline()
  	if err == nil && regxPattern.MatchString(line) {

			childPids, err := getChildPids(proc)
			if err == nil {
				for _, pid := range childPids {
					pids = append(pids, pid)
				}
			}
	  }
  }
  return pids, nil
}


//PidFile returns the pid from the pid file given.
func (pg *ParentFinder) PidFile(path string) ([]PID, error) {
	var pids []PID
	return pids, nil
}

//Pattern matches on the process name
func (pg *ParentFinder) Pattern(pattern string) ([]PID, error) {
	var pids []PID
	return pids, nil
}


//Uid will return all pids for the given user
func (pg *ParentFinder) Uid(user string) ([]PID, error) {
	var dst []PID
	return dst, nil
}


//Pattern matches on the parent process name
func (pg *ParentFinder) FullPattern(pattern string) ([]PID, error) {
	
	var pids []PID

	pids, err := getByParent(pattern)
	if err != nil {
		return pids, err
	}

	return pids, err
}
