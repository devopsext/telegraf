package procstat_boost

//TODO: Clarify what to do with borken gopsutils (disk_linux.go, process_linux.go - name resolving) -> Build from own feature branch of gopsutils
//TODO: Make pull request in gopsutil project

import (
	"bytes"
	"fmt"
	"github.com/pkg/errors"
	"io/ioutil"
	"log"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/influxdata/telegraf"
	"github.com/influxdata/telegraf/plugins/inputs"
	gops "github.com/mitchellh/go-ps"
	"github.com/shirou/gopsutil/process"
)

var (
	defaultPIDFinder = NewPgrepFinder
	defaultProcess   = NewProc

	// execCommand is so tests can mock out exec.Command usage.
	execCommand = exec.Command
)

type PID int32

type ProcstatBoost struct {
	PidFinder      string   `toml:"pid_finder"`
	PidFile        string   `toml:"pid_file"`
	Exe            string   `toml:"exe"`
	Pattern        string   `toml:"pattern"`
	XtraConfig     []string `toml:"xtra_config"`
	ProcessName    string   `toml:"process_name"`
	User           string   `toml:"user"`
	SystemdUnit    string   `toml:"systemd_unit"`
	CGroup         string   `toml:"cgroup"`
	Prefix         string   `toml:"prefix"`
	preparedPrefix string
	LimitMetrics   bool `toml:"limit_metrics"`

	//Either drop or emit metrics with incorrect CPU utilization value.
	//Emitting can be useful for debugging. emit_incorrect_cpu_values = false (default)
	//This parameter is not described in readme, intentionally
	EmitIncorrectCPUValues bool `toml:"emit_incorrect_cpu_values"` //This is for debugging only

	UpdateProcessTags bool   `toml:"update_process_tags"`
	PidTag            bool   `toml:"pid_tag"`
	WinService        string `toml:"win_service"`

	finder     PIDFinder
	finderTags map[string]string
	acc        telegraf.Accumulator

	createPIDFinder func(*ProcstatBoost) (PIDFinder, error)
	procs           map[PID]Process
	createProcess   func(PID) (Process, error)

	initialized       bool
	numCPU            int
	maxCPUUtilization float64
}

var sampleConfig = `
  ## Method to use when finding process IDs.  Can be one of:
  ## 'pgrep'- The pgrep finder calls the pgrep executable in the PATH while 
  ## 'native' - The native finder performs the search directly in a manor dependent on the platform.
  ## 'parent' - search by means of gopsutil based on the name of the parent process, regex is supported.
  ## Default is 'pgrep'
  # pid_finder = "pgrep"


  ## PID file to monitor process. Valid for pid_finders: pgrep, native
  # pid_file = "/var/run/nginx.pid" 
  
  ## executable name (ie, pgrep <exe>). Valid for pid_finders: pgrep, native. 
  ## For native finder, this is most optimized as github.com/mitchellh/go-ps used to get process list 
  # exe = "nginx"
  
  ## pattern as argument for pgrep (ie, pgrep -f <pattern>). Valid for pid_finders: pgrep, native, parent
  # pattern = "nginx"
  
  ## Extra configuration to search processes.  Valid for pid_finders: pgrep, parent
  ## For 'pgrep' finder: the array of string will be used as a cli arguments to pgrep call (ie, pgrep arg1, arg2...)
  ## xtra_config = ["-a","--inverse","-P 1"] ==> 'pgrep -a --inverse -P 1'	
  ## For 'parent' fider: the array of parent pids, which children should be included with flags
  ## xtra_config = ["926;inclusive;inverse","1;exclusive","31;exclusive;inverse"] This means, that into the
  ## monitored scope, the following processes will be included:
  ##   All running process:
  ##   - except PID 926 + all it's children,
  ##   - except PID 1
  ##   - except PID 31
  ## I.e. the template is: <PARENT PID>;inclusive/exclusive;inverse. Inclusive means that the parent itself is included,
  ## exclusive, means only children are included.
  ## Also instead of <PARENT PID> there can be regex specified, to match against BINARY names.
  # xtra_config = ["-a","--inverse","-P 1"]
  
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

  ## Limit metrics. Allow to gather only very basic metrics for process. Default value is false.
  # limit_metrics = true
  
  ## If set to 'true', every gather interval, for every PID in scope, the cmdline & name tags will be updated (this produce additional CPU load).
  # update_process_tags = false

  ## Add PID as a tag instead of a field; useful to differentiate between
  ## processes whose tags are otherwise the same.  Can create a large number
  ## of series, use judiciously.
  # pid_tag = false
`

func (p *ProcstatBoost) SampleConfig() string {
	return sampleConfig
}

func (p *ProcstatBoost) Description() string {
	return "Monitor process cpu and memory usage"
}

func (p *ProcstatBoost) initialize(acc telegraf.Accumulator) error {

	if p.initialized {
		return nil
	}

	var err error
	p.acc = acc
	if p.createPIDFinder == nil { //function can be set already, when running tests...
		switch p.PidFinder {
		case "native":
			p.createPIDFinder = NewNativeFinder
		case "pgrep":
			p.createPIDFinder = NewPgrepFinder
		case "parent":
			p.createPIDFinder = NewParentFinder
		default:
			p.createPIDFinder = defaultPIDFinder
		}
		p.createProcess = defaultProcess
	}

	if p.Prefix != "" {
		p.preparedPrefix = p.Prefix + "_"
	}

	p.finder, err = p.createPIDFinder(p)
	if err != nil {
		return err
	}

	//Building pid finder tags
	if p.PidFile != "" {
		p.finderTags = map[string]string{"pidfile": p.PidFile}
	} else if p.Exe != "" {
		p.finderTags = map[string]string{"exe": p.Exe}
	} else if p.Pattern != "" {
		p.finderTags = map[string]string{"pattern": p.Pattern}
	} else if len(p.XtraConfig) != 0 {
		p.finderTags = map[string]string{"xtra_config": strings.Join(p.XtraConfig, ",")}
	} else if p.User != "" {
		p.finderTags = map[string]string{"user": p.User}
	} else if p.SystemdUnit != "" {
		p.finderTags = map[string]string{"systemd_unit": p.SystemdUnit}
	} else if p.CGroup != "" {
		p.finderTags = map[string]string{"cgroup": p.CGroup}
	} else if p.WinService != "" {
		p.finderTags = map[string]string{"win_service": p.WinService}
	} else {
		return errors.Errorf("Either exe, pid_file, user, pattern, xtra_config, systemd_unit, cgroup, or win_service must be specified")
	}

	p.numCPU = runtime.NumCPU()
	p.maxCPUUtilization = float64(p.numCPU * 100) //Max cpu utilization = 100% (full utilization of 1 CPU) * CPU count.

	p.initialized = true
	return nil
}

func (p *ProcstatBoost) Gather(acc telegraf.Accumulator) error {

	var err error
	var pids []PID
	metrics := map[float64]map[string]interface{}{}

	err = p.initialize(acc)
	if err != nil {
		log.Printf("E! [procstat_boost] Can't initialize input!")
		acc.AddError(err)
		return err
	}

	fields := map[string]interface{}{}

	//Get actual pids via pid finder that actual for specific settings
	pids, err = p.findPids()
	if err != nil {
		acc.AddError(fmt.Errorf("Can't get PID list: exe: [%s] pidfile: [%s] pattern: [%s] xtra_config: [%s] user: [%s] %s",
			p.Exe, p.PidFile, p.Pattern, p.XtraConfig, p.User, err.Error()))
		return err
	}
	//adds a metric with info of overall pid count
	acc.AddFields("procstat_boost_lookup",
		map[string]interface{}{"pid_count": len(pids)},
		map[string]string{"pid_finder": p.PidFinder})

	procs := make(map[PID]Process, len(p.procs))

	for _, pid := range pids {
		proc, ok := p.procs[pid]
		if ok {
			procs[pid] = proc

			if p.UpdateProcessTags {
				//Update proc tags, if  smth. changed
				gopsProcess, err := gops.FindProcess(int(pid))
				name := gopsProcess.Executable()
				if err == nil && proc.Tags()["process_name"] != name {
					//Updating name
					proc.Tags()["process_name"] = name

					//Updating cmd_line
					line, err := proc.Cmdline()
					if err == nil {
						proc.Tags()["process_cmdline"] = line
					}
				}
			}

		} else {
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
			} else {
				name, err := proc.Name()
				if err == nil {
					proc.Tags()["process_name"] = name
				}

			}

			//Enrichment:
			if _, ok := proc.Tags()["user"]; !ok {
				usr, err := proc.Username()
				if err == nil {
					proc.Tags()["user"] = usr
				}
			}

			line, err := proc.Cmdline()
			if err == nil {
				proc.Tags()["process_cmdline"] = line
			}

		}
		//proc - process info (either that was found in previous list or a new one)
		//pid - current process pid
		cpuUsageDetectedCorrectly := false

		//Read metrics for the first time
		if !p.LimitMetrics {
			p.addMetrics(proc, p.preparedPrefix, fields, acc)
		} else {
			p.addLimitedMetrics(proc, p.preparedPrefix, fields, acc)
		}

		//Check cpu consistency with max retry limit:
		//gopsutil report incorrect CPU and memory utilization for PIDs when they are forked and died with
		//high frequency (50 ms). In a nut shell: gopsutil attaches figures of already died process to the other
		//real alive processes in the pool (randomly chosen), so this produced unusable fake metrics values.
		//The fix identifies incorrect values and remove them from the reported data.

		if val, hasCPU := fields[p.preparedPrefix+"cpu_usage"]; hasCPU && val.(float64) > 0 {
			if procInfo, ok := metrics[fields[p.preparedPrefix+"cpu_usage"].(float64)]; ok || //Duplicates found
				fields[p.preparedPrefix+"cpu_usage"].(float64) > p.maxCPUUtilization { // Or go-ps util report unrealistic CPU utilization

				//Printing out error only in case that retries didn't fix the incorrect value
				if fields[p.preparedPrefix+"cpu_usage"].(float64) < p.maxCPUUtilization {
					proc.Tags()["dpl_info"] = fmt.Sprintf("retry: %d, duplicate value:"+
						"new: pid: %d, name: %s, CPU: %f "+
						"old: pid: %d, name: %s!",
						0,
						pid, proc.Tags()["process_name"], fields[p.preparedPrefix+"cpu_usage"].(float64),
						procInfo["pid"].(PID), procInfo["name"].(string))

					log.Printf("E! [inputs.procstat_boost] ==>retry: %d, duplicate value:\n "+
						"new: pid: %d, name: %s, CPU: %f\n "+
						"old: pid: %d, name: %s\n! <==",
						0,
						pid, proc.Tags()["process_name"], fields[p.preparedPrefix+"cpu_usage"].(float64),
						procInfo["pid"].(PID), procInfo["name"].(string))

				} else {
					proc.Tags()["unrl_cpu_info"] = fmt.Sprintf("retry: %d, unrealistic CPU utilization: "+
						"pid: %d, name: %s, CPU: %f, max CPU utilization: %f",
						0,
						pid, proc.Tags()["process_name"], fields[p.preparedPrefix+"cpu_usage"].(float64), p.maxCPUUtilization)

					log.Printf("E! [inputs.procstat_boost] ==>retry: %d, unrealistic CPU utilization: "+
						"pid: %d, name: %s, CPU: %f, max CPU utilization: %f\n",
						0,
						pid, proc.Tags()["process_name"], fields[p.preparedPrefix+"cpu_usage"].(float64), p.maxCPUUtilization)
				}

			} else { //No errors
				//Add gathered information to metrics map, for further checks
				metrics[fields[p.preparedPrefix+"cpu_usage"].(float64)] = map[string]interface{}{
					"pid":  pid,
					"name": proc.Tags()["process_name"]}
				cpuUsageDetectedCorrectly = true
			}

		} else { //No data about CPU found
			cpuUsageDetectedCorrectly = true
		}

		//Add metrics
		if cpuUsageDetectedCorrectly || p.EmitIncorrectCPUValues {
			acc.AddFields("procstat_boost", fields, proc.Tags())
		} else {
			acc.AddFields("procstat_boost_dropped", fields, proc.Tags())
		}
	}
	//Update procs list
	p.procs = procs

	return nil
}

// Add metrics a single Process
func (p *ProcstatBoost) addMetrics(proc Process, prefix string, fields map[string]interface{}, acc telegraf.Accumulator) {

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

	faults, err := proc.PageFaults()
	if err == nil {
		fields[prefix+"minor_faults"] = faults.MinorFaults
		fields[prefix+"major_faults"] = faults.MajorFaults
		fields[prefix+"child_minor_faults"] = faults.ChildMinorFaults
		fields[prefix+"child_major_faults"] = faults.ChildMajorFaults
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

	mem_perc, err := proc.MemoryPercent()
	if err == nil {
		fields[prefix+"memory_usage"] = mem_perc
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
}

// Add limited metrics a single Process
func (p *ProcstatBoost) addLimitedMetrics(proc Process, prefix string, fields map[string]interface{}, acc telegraf.Accumulator) {

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
}

// Get matching PIDs and their initial tags
func (p *ProcstatBoost) findPids() ([]PID, error) {
	var pids []PID
	var err error

	if p.PidFile != "" {
		pids, err = p.finder.PidFile(p.PidFile)
	} else if p.Exe != "" {
		pids, err = p.finder.Exe(p.Exe)
	} else if p.Pattern != "" {
		pids, err = p.finder.Pattern(p.Pattern)
	} else if len(p.XtraConfig) != 0 {
		pids, err = p.finder.XtraConfig(p.XtraConfig)
	} else if p.User != "" {
		pids, err = p.finder.Uid(p.User)
	} else if p.SystemdUnit != "" {
		pids, err = p.systemdUnitPIDs()
	} else if p.CGroup != "" {
		pids, err = p.cgroupPIDs()
	} else if p.WinService != "" {
		pids, err = p.winServicePIDs()
	} else {
		err = fmt.Errorf("Either exe, pid_file, user, pattern, systemd_unit, cgroup, or win_service must be specified")
	}

	return pids, err
}

func (p *ProcstatBoost) systemdUnitPIDs() ([]PID, error) {
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

func (p *ProcstatBoost) cgroupPIDs() ([]PID, error) {
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

func (p *ProcstatBoost) winServicePIDs() ([]PID, error) {
	var pids []PID

	pid, err := queryPidWithWinServiceName(p.WinService)
	if err != nil {
		return pids, err
	}

	pids = append(pids, PID(pid))

	return pids, nil
}

func init() {
	inputs.Add("procstat_boost", func() telegraf.Input { return &ProcstatBoost{} })
}
