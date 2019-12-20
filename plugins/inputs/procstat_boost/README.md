# Boosted Procstat Input Plugin (optimized for *nix based OSes)
The primary motivation of this plugin is to eliminate side-effects and issues of gopsutil (see below in detail) and ineffective design
to reduce CPU load. This plugin was optimnized to scrap 1000+ pid (simultaneously) with high fork rate under *nix OSes.
Tested in the following production environments:
 - as a part of DaemonSet to monitor worker and master nodes (`k8s` case)
 - as a process inside container to monitor primary PIDs (`rancher`/`docker` case)

`gopsutil` issues and side-effects:
1. `gopsutil` report incorrect CPU and memory utilization for PIDs when they are forked and died with 
high frequency (50 ms). In a nut shell: `gopsutil` attaches figures of already died process to the other 
real alive processes in the pool (randomly chosen), so this produced unusable fake metrics values.
The fix identifies incorrect values and remove them from the reported data.
 
2. Minor tweaks to eliminate side-effects  & extensions:
 - new pid finder 'parent' added to select PIDs for monitoring processes only for specific parent
 - support for negative regex expression in native finder (`github.com/dlclark/regexp2`)
 - switched to `github.com/mitchellh/go-ps` to query PID list, as it works faster and with less CPU consumption then `gopsutil`
 - refresh process name tag while re-reading statistics - useful in case first read returns grabage (kind of workaround for gopsutil)
 - enable limited metrics gathering that reduce CPU utilisation.

This plugin can be used to monitor the system resource usage of one or more processes. The procstat_lookup metric displays the query information, 
specifically the number of PIDs returned on a search

Processes can be selected for monitoring using `finders`. 
There are following finders available:
 * `pgrep`- The pgrep finder calls the pgrep executable in the PATH to get info about processes (NOT RECOMMENDED UNDER PRESSURE) 
 * `native` - The native finder performs the search directly in a manner dependent on the platform (Preffered, with settings via `exe` attribute).
 * `parent` - search by means of `gopsutil` and `github.com/mitchellh/go-ps` (depending on the settings) based 
 on the name of the parent process, regex is supported.

Each finder can be configured via following attributes:

<table>

| __attrib/finder__    | pgrep | native | parent |
|--------------|-------|--------|--------|
| pidfile      |   +   |    +   |        |
| exe          |   +   |    +   |        |
| pattern      |   +   |    +   |    +   |
| xtra_config     |   !+  |        |    +   |
| user         |   +   |    +   |        |
| systemd_unit |   +   |        |        |
| cgroup       |   +   |        |        |
| win_service  |       |    +   |        |
</table>

### Configuration:

```toml
[[inputs.procstat_boost]]
  
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
```

#### Windows support

Preliminary support for Windows has been added, however you may prefer using
the `win_perf_counters` input plugin as a more mature alternative.

When using the `pid_finder = "native"` in Windows, the pattern lookup method is
implemented as a WMI query.  The pattern allows fuzzy matching using only
[WMI query patterns](https://msdn.microsoft.com/en-us/library/aa392263(v=vs.85).aspx):
```toml
[[inputs.procstat_boost]]
  pattern = "%influx%"
  pid_finder = "native"
```

### Metrics:

- procstat_boost
  - tags:
    - pid (when `pid_tag` is true)
    - process_name
    - pidfile (when defined)
    - exe (when defined)
    - pattern (when defined)
    - user (when selected)
    - systemd_unit (when defined)
    - cgroup (when defined)
    - win_service (when defined)
  - fields:
    - cpu_time (int)
    - cpu_time_guest (float)
    - cpu_time_guest_nice (float)
    - cpu_time_idle (float)
    - cpu_time_iowait (float)
    - cpu_time_irq (float)
    - cpu_time_nice (float)
    - cpu_time_soft_irq (float)
    - cpu_time_steal (float)
    - cpu_time_stolen (float)
    - cpu_time_system (float)
    - cpu_time_user (float)
    - cpu_usage (float)
    - involuntary_context_switches (int)
    - memory_data (int)
    - memory_locked (int)
    - memory_rss (int)
    - memory_stack (int)
    - memory_swap (int)
    - memory_vms (int)
    - nice_priority (int)
    - num_fds (int, *telegraf* may need to be ran as **root**)
    - num_threads (int)
    - pid (int)
    - read_bytes (int, *telegraf* may need to be ran as **root**)
    - read_count (int, *telegraf* may need to be ran as **root**)
    - realtime_priority (int)
    - rlimit_cpu_time_hard (int)
    - rlimit_cpu_time_soft (int)
    - rlimit_file_locks_hard (int)
    - rlimit_file_locks_soft (int)
    - rlimit_memory_data_hard (int)
    - rlimit_memory_data_soft (int)
    - rlimit_memory_locked_hard (int)
    - rlimit_memory_locked_soft (int)
    - rlimit_memory_rss_hard (int)
    - rlimit_memory_rss_soft (int)
    - rlimit_memory_stack_hard (int)
    - rlimit_memory_stack_soft (int)
    - rlimit_memory_vms_hard (int)
    - rlimit_memory_vms_soft (int)
    - rlimit_nice_priority_hard (int)
    - rlimit_nice_priority_soft (int)
    - rlimit_num_fds_hard (int)
    - rlimit_num_fds_soft (int)
    - rlimit_realtime_priority_hard (int)
    - rlimit_realtime_priority_soft (int)
    - rlimit_signals_pending_hard (int)
    - rlimit_signals_pending_soft (int)
    - signals_pending (int)
    - voluntary_context_switches (int)
    - write_bytes (int, *telegraf* may need to be ran as **root**)
    - write_count (int, *telegraf* may need to be ran as **root**)
- procstat_boost_lookup
  - tags:
    - exe (string)
    - pid_finder (string)
    - pid_file (string)
    - pattern (string)
    - prefix (string)
    - user (string)
    - systemd_unit (string)
    - cgroup (string)
    - win_service (string)
  - fields:
    - pid_count (int)
*NOTE: Resource limit > 2147483647 will be reported as 2147483647.*

### Example Output:

```
procstat_boost,pidfile=/var/run/lxc/dnsmasq.pid,process_name=dnsmasq rlimit_file_locks_soft=2147483647i,rlimit_signals_pending_hard=1758i,voluntary_context_switches=478i,read_bytes=307200i,cpu_time_user=0.01,cpu_time_guest=0,memory_swap=0i,memory_locked=0i,rlimit_num_fds_hard=4096i,rlimit_nice_priority_hard=0i,num_fds=11i,involuntary_context_switches=20i,read_count=23i,memory_rss=1388544i,rlimit_memory_rss_soft=2147483647i,rlimit_memory_rss_hard=2147483647i,nice_priority=20i,rlimit_cpu_time_hard=2147483647i,cpu_time=0i,write_bytes=0i,cpu_time_idle=0,cpu_time_nice=0,memory_data=229376i,memory_stack=135168i,rlimit_cpu_time_soft=2147483647i,rlimit_memory_data_hard=2147483647i,rlimit_memory_locked_hard=65536i,rlimit_signals_pending_soft=1758i,write_count=11i,cpu_time_iowait=0,cpu_time_steal=0,cpu_time_stolen=0,rlimit_memory_stack_soft=8388608i,cpu_time_system=0.02,cpu_time_guest_nice=0,rlimit_memory_locked_soft=65536i,rlimit_memory_vms_soft=2147483647i,rlimit_file_locks_hard=2147483647i,rlimit_realtime_priority_hard=0i,pid=828i,num_threads=1i,cpu_time_soft_irq=0,rlimit_memory_vms_hard=2147483647i,rlimit_realtime_priority_soft=0i,memory_vms=15884288i,rlimit_memory_stack_hard=2147483647i,cpu_time_irq=0,rlimit_memory_data_soft=2147483647i,rlimit_num_fds_soft=1024i,signals_pending=0i,rlimit_nice_priority_soft=0i,realtime_priority=0i
procstat_boost,exe=influxd,process_name=influxd rlimit_num_fds_hard=16384i,rlimit_signals_pending_hard=1758i,realtime_priority=0i,rlimit_memory_vms_hard=2147483647i,rlimit_signals_pending_soft=1758i,cpu_time_stolen=0,rlimit_memory_stack_hard=2147483647i,rlimit_realtime_priority_hard=0i,cpu_time=0i,pid=500i,voluntary_context_switches=975i,cpu_time_idle=0,memory_rss=3072000i,memory_locked=0i,rlimit_nice_priority_soft=0i,signals_pending=0i,nice_priority=20i,read_bytes=823296i,cpu_time_soft_irq=0,rlimit_memory_data_hard=2147483647i,rlimit_memory_locked_soft=65536i,write_count=8i,cpu_time_irq=0,memory_vms=33501184i,rlimit_memory_stack_soft=8388608i,cpu_time_iowait=0,rlimit_memory_vms_soft=2147483647i,rlimit_nice_priority_hard=0i,num_fds=29i,memory_data=229376i,rlimit_cpu_time_soft=2147483647i,rlimit_file_locks_soft=2147483647i,num_threads=1i,write_bytes=0i,cpu_time_steal=0,rlimit_memory_rss_hard=2147483647i,cpu_time_guest=0,cpu_time_guest_nice=0,cpu_usage=0,rlimit_memory_locked_hard=65536i,rlimit_file_locks_hard=2147483647i,involuntary_context_switches=38i,read_count=16851i,memory_swap=0i,rlimit_memory_data_soft=2147483647i,cpu_time_user=0.11,rlimit_cpu_time_hard=2147483647i,rlimit_num_fds_soft=16384i,rlimit_realtime_priority_soft=0i,cpu_time_system=0.27,cpu_time_nice=0,memory_stack=135168i,rlimit_memory_rss_soft=2147483647i
```
