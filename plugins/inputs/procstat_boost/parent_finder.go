package procstat_boost

import (
	"errors"
	"fmt"
	"github.com/dlclark/regexp2"
	gops "github.com/mitchellh/go-ps"
	"github.com/shirou/gopsutil/process"
	"log"
	"strconv"
	"strings"
)

//ParentFinder uses gops to find processes
type ParentFinder struct {
	regexPattern *regexp2.Regexp
	parsedAdData map[int]map[string]interface{}
}

//NewParentFinder ...
func NewParentFinder(procstat *ProcstatBoost) (PIDFinder, error) {
	var err error
	//Checking if regex is valid
	if procstat.Pattern != "" && len(procstat.XtraConfig) > 0 {
		return nil, errors.New("settings ambiguity: 'pattern' & 'xtra_config' are mutually exclusive")
	} else if procstat.Pattern == "" && len(procstat.XtraConfig) == 0 {
		return nil, errors.New("'pattern' or 'add_data' should be specified for 'parent' finder")
	}

	parentFinder := ParentFinder{}

	if procstat.Pattern != "" {
		parentFinder.regexPattern = regexp2.MustCompile(procstat.Pattern, regexp2.None)

		if err != nil {
			return nil, err
		}
	}

	if len(procstat.XtraConfig) > 0 {
		parentFinder.parsedAdData = map[int]map[string]interface{}{}
		for _, item := range procstat.XtraConfig {
			s := strings.Split(item, ";")
			elemCount := len(s)
			if len(s) == 0 {
				return nil, fmt.Errorf("Can't parse 'add_data' element '%s'", item)
			} else {
				pids := []PID{}
				pidInfo := map[PID]string{}
				pid, err := strconv.Atoi(s[0])

				if err != nil {
					//Try to find s[0] among executable
					regexPattern := regexp2.MustCompile(s[0], regexp2.None)
					procs, err := gops.Processes()
					if err != nil {
						return nil, errors.New("Can't get process list")
					}
					for _, proc := range procs {
						matched, err := regexPattern.MatchString(proc.Executable())
						if err != nil {
							return nil, fmt.Errorf("Error during matching executable name against regex, reason %v", err)
						}
						if matched {
							pids = append(pids, PID(proc.Pid()))
							pidInfo[PID(proc.Pid())] = proc.Executable()
						} else {
							log.Printf("D! [inputs.procstat_boost] process : '%d,%s', not matched against regex '%s'", proc.Pid(), proc.Executable(), s[0])
						}

					}
					//return nil, errors.New(fmt.Sprintf("Can't converse '%s' to int, 'add_data' element '%s'", s[0], item))
				} else {

					pids = append(pids, PID(pid))
				}

				for _, pid := range pids {
					parentFinder.parsedAdData[int(pid)] = map[string]interface{}{}
					//Default values
					parentFinder.parsedAdData[int(pid)]["inclusion"] = true
					parentFinder.parsedAdData[int(pid)]["inversion"] = false
					for i := 1; i < elemCount; i++ {
						if s[i] == "inclusive" {
							parentFinder.parsedAdData[int(pid)]["inclusion"] = true
						} else if s[i] == "exclusive" {
							parentFinder.parsedAdData[int(pid)]["inclusion"] = false
						} else if s[i] == "inverse" {
							parentFinder.parsedAdData[int(pid)]["inversion"] = true
						}

					}
					parentFinder.parsedAdData[int(pid)]["executable"] = pidInfo[pid]
				}

			}

		}

	}

	log.Printf("D! [inputs.procstat_boost] Built filter list based on add_data:")
	for pid, info := range parentFinder.parsedAdData {
		log.Printf("D! [inputs.procstat_boost] pid: %d, binary: %s, inclusion: %t, inversion: %t",
			pid, info["executable"].(string), info["inclusion"].(bool), info["inversion"].(bool))
	}
	return &parentFinder, nil
}

func (pf *ParentFinder) getChildPids(p *process.Process) ([]PID, error) {

	var pids []PID

	children, err := p.Children()
	if err != nil {
		return pids, err
	}

	for _, child := range children {
		pids = append(pids, PID(child.Pid))

		childPids, err := pf.getChildPids(child)
		if err == nil {

			for _, pid := range childPids {
				pids = append(pids, PID(pid))
			}
		}
	}
	return pids, nil
}

//Exe matches on the parent process name
func (pf *ParentFinder) Pattern(pattern string) ([]PID, error) {

	var pids []PID
	var err error

	procs, err := process.Processes()
	if err != nil {
		return pids, err
	}

	for _, proc := range procs {

		line, err := proc.Cmdline()
		if err != nil {
			//log.Printf("E! [inputs.procstat_boost] Can't get cmdline for process with PID '%d', skipping. Reason: %v\n",proc.Pid,err,line)
			continue
		}

		matched, err := pf.regexPattern.MatchString(line)
		if err != nil {
			log.Printf("E! [inputs.procstat_boost] Can't check regex match of cmdline '%s', skipping this process (PID - '%d'). Reason: %v\n", line, proc.Pid, err)
			continue
		}

		if matched {
			childPids, err := pf.getChildPids(proc)
			if err == nil {
				pids = append(pids, childPids...)
			}
		}
	}
	return pids, nil
}

//Added by IP
func (pf *ParentFinder) GetChildren(parentMap *map[PID][]PID, pidsMap *map[PID]PID, parent PID) {

	if children, ok := (*parentMap)[parent]; ok {
		//pids = append(pids,children...)
		for _, child := range children {
			(*pidsMap)[child] = child
			pf.GetChildren(parentMap, pidsMap, child)
		}
	}
}

//Xtra Config
func (pf *ParentFinder) XtraConfig(add_data []string) ([]PID, error) {
	//xtra_config = ["926;inclusive;inverse","1;exclusive","31;exclusive;inverse"] This means, that into the
	//monitored scope, the following processes will be included:
	//   All running process:
	//   - except PID 926 + all it's children,
	//   - except PID 1
	//   - except PID 31
	//I.e. the template is: <PARENT PID>;inclusive/exclusive;inverse. Inclusive means that the parent itself is included,
	//exclusive, means only children are included.
	//Also instead of <PARENT PID> there can be regex specified, to match against BINARY names.
	var pids []PID
	pidsMap := map[PID]PID{}
	excludedPidsMap := map[PID]PID{}
	parentMap := map[PID][]PID{}

	procs, err := gops.Processes()
	if err != nil {
		return pids, err
	}

	//Build parents list
	for _, proc := range procs {
		parentMap[PID(proc.PPid())] = append(parentMap[PID(proc.PPid())], PID(proc.Pid()))
	}
	log.Printf("D! [inputs.procstat_boost] Parent list len: %d", len(parentMap))

	if err != nil {
		return pids, err
	}
	//Build included and excluded pids based on filters:
	for pid, filter := range pf.parsedAdData {
		if filter["inversion"].(bool) {

			if filter["inclusion"].(bool) {
				excludedPidsMap[PID(pid)] = PID(pid)
			} else {
				pidsMap[PID(pid)] = PID(pid)
			}

			//Get children (recursively)
			pf.GetChildren(&parentMap, &excludedPidsMap, PID(pid))

		} else {

			if filter["inclusion"].(bool) {
				pidsMap[PID(pid)] = PID(pid)
			} else {
				excludedPidsMap[PID(pid)] = PID(pid)
			}

			//Get children (recursively)
			pf.GetChildren(&parentMap, &pidsMap, PID(pid))
		}
	}
	pidsMapLen := len(pidsMap)
	excludedPidsMapLen := len(excludedPidsMap)
	log.Printf("D! [inputs.procstat_boost] Included pids map len: %d", pidsMapLen)
	log.Printf("D! [inputs.procstat_boost] Excluded pids map len: %d", excludedPidsMapLen)

	for _, proc := range procs {
		pid := proc.Pid()
		//Check for pid inclusion

		if pidsMapLen != 0 { // list is not empty, check...
			if _, ok := pidsMap[PID(pid)]; !ok {
				continue
			} // Pid is NOT in included list, should be filtered
		}

		if excludedPidsMapLen != 0 { // list is not empty, check...
			if _, ex_ok := excludedPidsMap[PID(pid)]; ex_ok {
				continue
			} //Pid is IN excluded list, should be filtered
		}

		pids = append(pids, PID(pid))
	}

	return pids, nil
}

//Uid will return all pids for the given user
func (pf *ParentFinder) Uid(user string) ([]PID, error) {
	var dst []PID
	return dst, nil
}

//PidFile returns the pid from the pid file given.
func (pf *ParentFinder) PidFile(path string) ([]PID, error) {
	var pids []PID
	return pids, nil
}

//Exe matches on the process name
func (pf *ParentFinder) Exe(pattern string) ([]PID, error) {
	var pids []PID
	return pids, nil
}
