package procstat

import (
	"bytes"
	"fmt"
	"github.com/pkg/errors"
	"io/ioutil"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/influxdata/telegraf"
	"github.com/influxdata/telegraf/plugins/inputs"
	"github.com/shirou/gopsutil/process"
	gops "github.com/mitchellh/go-ps"
)

var (
	defaultPIDFinder = NewPgrep
	defaultProcess   = NewProc

	// execCommand is so tests can mock out exec.Command usage.
	execCommand = exec.Command
)

type PID int32

type Procstat struct {
	PidFinder		string 		`toml:"pid_finder"`
	PidFile     	string 		`toml:"pid_file"`
	Exe         	string 		`toml:"exe"`
	Pattern     	string 		`toml:"pattern"`
	AddData			[]string	`toml:"add_data"` //Added by IP
	ProcessName		string		`toml:"process_name"`
	User        	string		`toml:"user"`
	SystemdUnit 	string		`toml:"systemd_unit"`
	CGroup      	string		`toml:"cgroup"`
	Prefix      	string		`toml:"prefix"`
	preparedPrefix	string
	LimitMetrics	bool		`toml:"limit_metrics"`
	UpdateProcessTags bool		`toml:"update_process_tags"`
	PidTag      	bool		`toml:"pid_tag"`
	WinService  	string		`toml:"win_service"`

	finder PIDFinder
	finderTags		map[string]string
	acc telegraf.Accumulator

	createPIDFinder func(*Procstat) (PIDFinder, error)
	procs           map[PID]Process
	createProcess   func(PID) (Process, error)
}

var sampleConfig = `
  ## PID file to monitor process. Valid for pid_finders: pgrep, native
  # pid_file = "/var/run/nginx.pid" 
  
  ## executable name (ie, pgrep <exe>). Valid for pid_finders: pgrep, native
  # exe = "nginx"
  
  ## pattern as argument for pgrep (ie, pgrep -f <pattern>). Valid for pid_finders: pgrep, native, parent
  # pattern = "nginx"
  
  ## additional data to search processes.  Valid for pid_finders: pgrep, parent
  ## For 'pgrep' finder: the array of string will be used as a cli arguments to pgrep call (ie, pgrep arg1, arg2...)
  ## add_data = ["-a","--inverse","-P 1"] ==> 'pgrep -a --inverse -P 1'	
  ## For 'parent' fider: the array of parent pids, which children should be included with flags
  ## add_data = ["926;inclusive;inverse","1;exclusive","31;exclusive;inverse"] This means, that into the monitored scope, the following processes will be included:
  ## All running process:
  ## - except PID 926 + all it's children,
  ## - except PID 1
  ## - except PID 31
  ## I.e. the template is: <PARENT PID>;inclusive/exclusive;inverse. Inclusive means that the parent itself is included, exclusive, means only children are included.
  ## Also instead of <PARENT PID> there can be regex specified, to match against BINARY names.
  # add_data = ["-a","--inverse","-P 1"]
  
  ## user as argument for pgrep (ie, pgrep -u <user>). Valid for pid_finders: pgrep, native
  # user = "nginx"
  
  ## Systemd unit name. Valid for pid_finders: pgrep
  # systemd_unit = "nginx.service"
  
  ## CGroup name or path. Valid for pid_finders: pgrep
  # cgroup = "systemd/system.slice/nginx.service"

  ## Windows service name. Valid for pid_finders: native
  # win_service = ""

  ## override for process_name
  ## This is optional; default is sourced from /proc/<pid>/status
  # process_name = "bar"

  ## Field name prefix
  # prefix = ""

  ## Limit metrics. Allow to gather only very basic metrics for process. Default values is false.
  # limit_metrics = true
  
  ## If set to 'true', every gather interval, for every PID in scope, the cmdline & name tags will be updated (this produce additional CPU load).
  # update_process_tags = false

  ## Add PID as a tag instead of a field; useful to differentiate between
  ## processes whose tags are otherwise the same.  Can create a large number
  ## of series, use judiciously.
  # pid_tag = false

  ## Method to use when finding process IDs.  Can be one of:
  ## 'pgrep'- The pgrep finder calls the pgrep executable in the PATH while 
  ## 'native' - The native finder performs the search directly in a manor dependent on the platform.
  ## 'parent' - search by means of gopsutil based on the name of the parent process, regex is supported.
  ## Default is 'pgrep'
  # pid_finder = "pgrep"
`



func (p *Procstat) SampleConfig() string {
	return sampleConfig
}

func (p *Procstat) Description() string {
	return "Monitor process cpu and memory usage"
}

func (p *Procstat) Gather(acc telegraf.Accumulator) error {

	var err error
	fields := map[string]interface{}{}

	//Get actual pids via pid finder + some tags that actual for specific pid finder
	pids, err := p.findPids(acc)
	if err != nil {
		acc.AddError(fmt.Errorf("E! Error: procstat getting process, exe: [%s] pidfile: [%s] pattern: [%s] add_data: [%s] user: [%s] %s",
			p.Exe, p.PidFile, p.Pattern, p.AddData, p.User, err.Error()))
		return  err
	}
	//adds a metric with info of overall pid count
	acc.AddFields("procstat_lookup",
				map[string]interface{}{"pid_count":len(pids)},
				map[string]string{"pid_finder": p.PidFinder})

	procs := make(map[PID]Process, len(p.procs))

	for _, pid := range pids {
		proc, ok := p.procs[pid]
		if ok {
			procs[pid] = proc

			if p.UpdateProcessTags {
				//Update proc tags, if  smth. changed
				gopsProcess,err := gops.FindProcess(int(pid))
				name := gopsProcess.Executable()
				if err == nil && proc.Tags()["process_name"] != name {
					//Updating name
					proc.Tags()["process_name"] = name

					//Updating cmd_line
					line,err := proc.Cmdline()
					if err == nil {
						proc.Tags()["process_cmdline"] = line
					}
				}
			}

		}else
		{
			proc, err = p.createProcess(pid)
			if err != nil {
				// No problem; process may have ended after we found it
				continue
			}
			procs[pid] = proc

			// Add initial tags (additiional tags that is setup when findPids is called)
			for k, v := range p.finderTags {
				proc.Tags()[k] = v
			}

			// Add tags
			if p.PidTag {
				proc.Tags()["pid"] = strconv.Itoa(int(pid))
			}
			if p.ProcessName != "" {
				proc.Tags()["process_name"] = p.ProcessName
			}else {
				name,err := proc.Name()
				if err == nil {
					proc.Tags()["process_name"] = name
				}

			}

			//Enrichment:
			usr,err := proc.Username()
			if err == nil {
				proc.Tags()["user"] = usr
			}
			line,err := proc.Cmdline()
			if err == nil {
				proc.Tags()["process_cmdline"] = line
			}

		}

		//proc - process info (either that was found in previous list or a new one)
		//pid - current process pid
		if ! p.LimitMetrics {
			p.addMetrics(proc, p.preparedPrefix,fields, acc)
		}else {
			p.addLimitedMetrics(proc, p.preparedPrefix,fields, acc)
		}

		//Add metrics
		acc.AddFields("procstat", fields, proc.Tags())

	}
	//Update procs list
	p.procs = procs

	return nil
}

func (p *Procstat) Start(acc telegraf.Accumulator) error{
	var err error
	p.acc = acc

	switch p.PidFinder {
		case "native":
			p.createPIDFinder = NewNativeFinder
		case "pgrep":
			p.createPIDFinder = NewPgrep
		case "parent":
			p.createPIDFinder = NewParentFinder
		default:
			p.createPIDFinder = defaultPIDFinder
		}

	p.createProcess = defaultProcess

	if p.Prefix != "" {
		p.preparedPrefix = p.Prefix + "_"
	}

	p.finder, err = p.createPIDFinder(p)
	if err != nil {
		return  err
	}

	//Building pid finder tags
	if p.PidFile != "" {
		p.finderTags = map[string]string{"pidfile": p.PidFile}
	} else if p.Exe != "" {
		p.finderTags = map[string]string{"exe": p.Exe}
	} else if p.Pattern != "" {
		p.finderTags = map[string]string{"pattern": p.Pattern}
	} else if len(p.AddData) != 0 {
		p.finderTags = map[string]string{"add_data":  strings.Join(p.AddData,",")}
	} else if p.User != "" {
		p.finderTags = map[string]string{"user": p.User}
	} else if p.SystemdUnit != "" {
		p.finderTags = map[string]string{"systemd_unit": p.SystemdUnit}
	} else if p.CGroup != "" {
		p.finderTags = map[string]string{"cgroup": p.CGroup}
	} else if p.WinService != "" {
		p.finderTags = map[string]string{"win_service": p.WinService}
	} else {
		return errors.New(fmt.Sprintf("Either exe, pid_file, user, pattern, add_data, systemd_unit, cgroup, or win_service must be specified"))
	}


	return nil
}

func (p *Procstat) Stop() {
	return
}

// Add metrics a single Process
func (p *Procstat) addMetrics(proc Process,prefix string, fields map[string]interface{},acc telegraf.Accumulator) {

	numThreads, err := proc.NumThreads()
	if err == nil {
		fields[prefix+"num_threads"] = numThreads
	}

	fds, err := proc.NumFDs()
	if err == nil {
		fields[prefix+"num_fds"] = fds
	}

	ctx, err := proc.NumCtxSwitches()
	if err == nil {
		fields[prefix+"voluntary_context_switches"] = ctx.Voluntary
		fields[prefix+"involuntary_context_switches"] = ctx.Involuntary
	}

	io, err := proc.IOCounters()
	if err == nil {
		fields[prefix+"read_count"] = io.ReadCount
		fields[prefix+"write_count"] = io.WriteCount
		fields[prefix+"read_bytes"] = io.ReadBytes
		fields[prefix+"write_bytes"] = io.WriteBytes
	}

	cpu_time, err := proc.Times()
	if err == nil {
		fields[prefix+"cpu_time_user"] = cpu_time.User
		fields[prefix+"cpu_time_system"] = cpu_time.System
		fields[prefix+"cpu_time_idle"] = cpu_time.Idle
		fields[prefix+"cpu_time_nice"] = cpu_time.Nice
		fields[prefix+"cpu_time_iowait"] = cpu_time.Iowait
		fields[prefix+"cpu_time_irq"] = cpu_time.Irq
		fields[prefix+"cpu_time_soft_irq"] = cpu_time.Softirq
		fields[prefix+"cpu_time_steal"] = cpu_time.Steal
		//fields[prefix+"cpu_time_stolen"] = cpu_time.Stolen //(not supported since gopsutil v2.18.12
		fields[prefix+"cpu_time_guest"] = cpu_time.Guest
		fields[prefix+"cpu_time_guest_nice"] = cpu_time.GuestNice
	}

	cpu_perc, err := proc.Percent(time.Duration(0))
	if err == nil {
		fields[prefix+"cpu_usage"] = cpu_perc
	}

	mem, err := proc.MemoryInfo()
	if err == nil {
		fields[prefix+"memory_rss"] = mem.RSS
		fields[prefix+"memory_vms"] = mem.VMS
		fields[prefix+"memory_swap"] = mem.Swap
		fields[prefix+"memory_data"] = mem.Data
		fields[prefix+"memory_stack"] = mem.Stack
		fields[prefix+"memory_locked"] = mem.Locked
	}

	rlims, err := proc.RlimitUsage(true)
	if err == nil {
		for _, rlim := range rlims {
			var name string
			switch rlim.Resource {
			case process.RLIMIT_CPU:
				name = "cpu_time"
			case process.RLIMIT_DATA:
				name = "memory_data"
			case process.RLIMIT_STACK:
				name = "memory_stack"
			case process.RLIMIT_RSS:
				name = "memory_rss"
			case process.RLIMIT_NOFILE:
				name = "num_fds"
			case process.RLIMIT_MEMLOCK:
				name = "memory_locked"
			case process.RLIMIT_AS:
				name = "memory_vms"
			case process.RLIMIT_LOCKS:
				name = "file_locks"
			case process.RLIMIT_SIGPENDING:
				name = "signals_pending"
			case process.RLIMIT_NICE:
				name = "nice_priority"
			case process.RLIMIT_RTPRIO:
				name = "realtime_priority"
			default:
				continue
			}

			fields[prefix+"rlimit_"+name+"_soft"] = rlim.Soft
			fields[prefix+"rlimit_"+name+"_hard"] = rlim.Hard
			if name != "file_locks" { // gopsutil doesn't currently track the used file locks count
				fields[prefix+name] = rlim.Used
			}
		}
	}

	//acc.AddFields("procstat", fields, proc.Tags())
}

// Add metrics a single Process
func (p *Procstat) addLimitedMetrics(proc Process,prefix string, fields map[string]interface{},acc telegraf.Accumulator) {

	cpu_perc, err := proc.Percent(time.Duration(0))
	if err == nil {
		fields[prefix+"cpu_usage"] = cpu_perc
	}

	mem, err := proc.MemoryInfo()
	if err == nil {
		fields[prefix+"memory_rss"] = mem.RSS
		fields[prefix+"memory_vms"] = mem.VMS
		fields[prefix+"memory_swap"] = mem.Swap
		fields[prefix+"memory_data"] = mem.Data
		fields[prefix+"memory_stack"] = mem.Stack
		fields[prefix+"memory_locked"] = mem.Locked
	}

	//acc.AddFields("procstat", fields, proc.Tags())
}

/*
// Update monitored Processes
func (p *Procstat) updateProcesses(acc telegraf.Accumulator, prevInfo map[PID]Process) (map[PID]Process, error) {
	pids, tags, err := p.findPids(acc)
	if err != nil {
		return nil, err
	}

	procs := make(map[PID]Process, len(prevInfo))

	for _, pid := range pids {
		info, ok := prevInfo[pid]
		if ok {
			procs[pid] = info
		} else {
			proc, err := p.createProcess(pid)
			if err != nil {
				// No problem; process may have ended after we found it
				continue
			}
			procs[pid] = proc

			// Add initial tags
			for k, v := range tags {
				proc.Tags()[k] = v
			}

			// Add pid tag if needed
			if p.PidTag {
				proc.Tags()["pid"] = strconv.Itoa(int(pid))
			}
			if p.ProcessName != "" {
				proc.Tags()["process_name"] = p.ProcessName
			}
		}
	}
	return procs, nil
}

// Create and return PIDGatherer lazily

func (p *Procstat) getPIDFinder() (PIDFinder, error) {
	if p.finder == nil {
		f, err := p.createPIDFinder()
		if err != nil {
			return nil, err
		}
		p.finder = f
	}
	return p.finder, nil
}
*/
// Get matching PIDs and their initial tags
func (p *Procstat) findPids(acc telegraf.Accumulator) ([]PID, error) {
	var pids []PID
	var err error

	if p.PidFile != "" {
		pids, err = p.finder.PidFile(p.PidFile)
		//tags = map[string]string{"pidfile": p.PidFile}
	} else if p.Exe != "" {
		pids, err = p.finder.Pattern(p.Exe)
		//tags = map[string]string{"exe": p.Exe}
	} else if p.Pattern != "" {
		pids, err = p.finder.FullPattern(p.Pattern)
		//tags = map[string]string{"pattern": p.Pattern}
	} else if len(p.AddData) != 0 {
		pids, err = p.finder.AddData(p.AddData)
		//tags = map[string]string{"add_data":  strings.Join(p.AddData,",")}
	} else if p.User != "" {
		pids, err = p.finder.Uid(p.User)
		//tags = map[string]string{"user": p.User}
	} else if p.SystemdUnit != "" {
		pids, err = p.systemdUnitPIDs()
		//tags = map[string]string{"systemd_unit": p.SystemdUnit}
	} else if p.CGroup != "" {
		pids, err = p.cgroupPIDs()
		//tags = map[string]string{"cgroup": p.CGroup}
	} else if p.WinService != "" {
		pids, err = p.winServicePIDs()
		//tags = map[string]string{"win_service": p.WinService}
	} else {
		err = fmt.Errorf("Either exe, pid_file, user, pattern, systemd_unit, cgroup, or win_service must be specified")
	}


	/* moved to Gather function
	//adds a metric with info on the pgrep query
	fields := make(map[string]interface{})
	tags["pid_finder"] = p.PidFinder
	fields["pid_count"] = len(pids)
	acc.AddFields("procstat_lookup", fields, tags)
	*/

	return pids, err
}

func (p *Procstat) systemdUnitPIDs() ([]PID, error) {
	var pids []PID
	cmd := execCommand("systemctl", "show", p.SystemdUnit)
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	for _, line := range bytes.Split(out, []byte{'\n'}) {
		kv := bytes.SplitN(line, []byte{'='}, 2)
		if len(kv) != 2 {
			continue
		}
		if !bytes.Equal(kv[0], []byte("MainPID")) {
			continue
		}
		if len(kv[1]) == 0 {
			return nil, nil
		}
		pid, err := strconv.Atoi(string(kv[1]))
		if err != nil {
			return nil, fmt.Errorf("invalid pid '%s'", kv[1])
		}
		pids = append(pids, PID(pid))
	}
	return pids, nil
}

func (p *Procstat) cgroupPIDs() ([]PID, error) {
	var pids []PID

	procsPath := p.CGroup
	if procsPath[0] != '/' {
		procsPath = "/sys/fs/cgroup/" + procsPath
	}
	procsPath = filepath.Join(procsPath, "cgroup.procs")
	out, err := ioutil.ReadFile(procsPath)
	if err != nil {
		return nil, err
	}
	for _, pidBS := range bytes.Split(out, []byte{'\n'}) {
		if len(pidBS) == 0 {
			continue
		}
		pid, err := strconv.Atoi(string(pidBS))
		if err != nil {
			return nil, fmt.Errorf("invalid pid '%s'", pidBS)
		}
		pids = append(pids, PID(pid))
	}

	return pids, nil
}

func (p *Procstat) winServicePIDs() ([]PID, error) {
	var pids []PID

	pid, err := queryPidWithWinServiceName(p.WinService)
	if err != nil {
		return pids, err
	}

	pids = append(pids, PID(pid))

	return pids, nil
}

func init() {
	inputs.Add("procstat", func() telegraf.Input {return &Procstat{}})
}
