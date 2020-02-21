package docker_cnt_logs

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"os/exec"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/filters"
	docker "github.com/docker/docker/client"
	"github.com/influxdata/telegraf"
	"github.com/influxdata/telegraf/filter"
	"github.com/influxdata/telegraf/internal"
	tlsint "github.com/influxdata/telegraf/internal/tls"
	"github.com/influxdata/telegraf/plugins/inputs"
	"github.com/pkg/errors"
)

const (
	inputTitle               = "inputs.docker_cnt_logs"
	defaultInitialChunkSize  = 1000
	defaultMaxChunkSize      = 5000
	dockerLogHeaderSize      = 8
	dockerTimeStampLength    = 30
	defaultLogGatherInterval = 2000 * time.Millisecond
	defaultFlushInterval     = 3 * time.Second

	sampleConfig = `
  ## Interval to gather data from docker sock.
  ## the longer the interval the fewer request is made towards docker API (less CPU utilization on dockerd).
  ## On the other hand, this increase the delay between producing logs and delivering it. Reasonable trade off
  ## should be chosen
  interval = "2000ms"
  
  ## Docker Endpoint
  ##  To use unix, set endpoint = "unix:///var/run/docker.sock" (/var/run/docker.sock is default mount path)
  ##  To use TCP, set endpoint = "tcp://[ip]:[port]"
  ##  To use environment variables (ie, docker-machine), set endpoint = "ENV"
  endpoint = "unix:///var/run/docker.sock"

  ## Optional TLS Config
  # tls_ca = "/etc/telegraf/ca.pem"
  # tls_cert = "/etc/telegraf/cert.pem"
  # tls_key = "/etc/telegraf/key.pem"

  ## Use TLS but skip chain & host verification
  # insecure_skip_verify = false

  ## Log streaming settings
  ## Set initial chunk size (length of []byte buffer to read from docker socket)
  ## If not set, default value of 'defaultInitialChunkSize = 1000' will be used
  # initial_chunk_size = 1000 # 1K symbols (half of 80x25 screen)

  ## Max chunk size (length of []byte buffer to read from docker socket)
  ## Buffer can grow in capacity adjusting to volume of data received from docker sock
  ## to the maximum volume limited by this parameter. The bigger buffer is set
  ## the more data potentially it can read during 1 API call to docker.
  ## And all of this data will be processed before sending, that increase CPU utilization.
  ## This parameter should be set carefully.
  # max_chunk_size = 5000 # 5K symbols

  ## Offset flush interval. How often the offset pointer (see below) in the
  ## log stream is flashed to file.Offset pointer represents the unix time stamp
  ## in nano seconds for the last message read from log stream (default - 3 sec)
  # offset_flush = "3s"

  ## Offset storage path (mandatory), make sure the user on behalf 
  ## of which the telegraf is started has appropriate rights to read and write to chosen path.
  offset_storage_path = "/var/run/collector_offset"

  ## Shutdown telegraf if all log streaming containers stopped/killed, default - false
  ## This option make sense when telegraf started especially for streaming logs
  ## in a form of sidecar container in k8s. In case primary container exited,
  ## side-car should be terminated also.
  # shutdown_when_eof = false

  ## Static settings per container (specify as many sections as needed)
  [[inputs.docker_cnt_logs.container]]
    ## Set container id (long or short from), or container name
    ## to stream logs from, this attribute is mandatory
    id = "dc23d3ea534b3a6ec3934ae21e2dd4955fdbf61106b32fa19b831a6040a7feef"

    ## Override common settings
    ## input interval (specified or inherited from agent section)
    # interval = "500ms"

    ## Initial chunk size
    initial_chunk_size = 2000 # 2K symbols

    ## Max chunk size
    max_chunk_size = 6000 # 6K symbols

    #Set additional tags that will be tagged to the stream from the current container:
    tags = [
        "tag1=value1",
        "tag2=value2"
    ]
  ##Another container to stream logs from  
  [[inputs.docker_cnt_logs.container]]
    id = "009d82030745c9994e2f5c2280571e8b9f95681793a8f7073210759c74c1ea36"
    interval = "600ms"
`
)

var (
	containerStates = []string{"created", "restarting", "running", "removing", "paused", "exited", "dead"}
	version         = "1.21" // Support as old version as possible
	defaultHeaders  = map[string]string{"User-Agent": "engine-api-cli-1.0"}

	//Ensure that docker.client implement dClient
	_ dClient = (*docker.Client)(nil)

	//Ensure *DockerCNTLogs implements telegaf.ServiceInput
	_ telegraf.ServiceInput = (*DockerCNTLogs)(nil)
)

// DockerCNTLogs object
type DockerCNTLogs struct {
	//Configuration parameters
	Endpoint              string            `toml:"endpoint"`
	FromBeginning         bool              `toml:"from_beginning"`
	Timeout               internal.Duration `toml:"timeout"`
	LabelInclude          []string          `toml:"docker_label_include"`
	LabelExclude          []string          `toml:"docker_label_exclude"`
	ContainerInclude      []string          `toml:"container_name_include"`
	ContainerExclude      []string          `toml:"container_name_exclude"`
	ContainerStateInclude []string          `toml:"container_state_include"`
	ContainerStateExclude []string          `toml:"container_state_exclude"`
	IncludeSourceTag      bool              `toml:"source_tag"`

	LogGatherInterval             internal.Duration      `toml:"log_gather_interval"`
	InitialChunkSize              int                    `toml:"initial_chunk_size"`
	MaxChunkSize                  int                    `toml:"max_chunk_size"`
	OffsetFlush                   string                 `toml:"offset_flush"`
	OffsetStoragePath             string                 `toml:"offset_storage_path"`
	WhenStaticContainersProcessed map[string]interface{} `toml:"when_static_container_processed"`

	StaticContainerList []map[string]interface{} `toml:"container"`

	tlsint.ClientConfig //Parsing is handled in tlsint module

	//Internal
	client                     dClient                    //dClient is wrapper for both - docker.client and mock client
	labelFilter                filter.Filter              //container label filter
	containerFilter            filter.Filter              //container name filter
	stateFilter                filter.Filter              //container state filter
	opts                       types.ContainerListOptions //Container list options with set filters
	disableTimeStampsStreaming bool                       //Used for simulating reading logs with or without TS (used in tests only)
	whenProcessedCommand       []string                   //Command to be run when all static containers are processed

	wg sync.WaitGroup //WG for the rest of go routines in the input
	//
	muContainerList                sync.Mutex                    //To quard containerList
	containerList                  map[string]context.CancelFunc //List of containers from which logs are tailed
	muProcessedContainerList       sync.Mutex                    //To guard processedContainerList
	processedContainerList         map[string]interface{}        //List of processed earlier containers
	processedContainersChan        chan map[string]interface{}   //Channel to broadcast processedContainerList
	processedContainersCheckerDone chan bool                     //Channel for signalling processed container checker go routine to stop

	//For sync & communicate with streamers:
	wgStreamers         sync.WaitGroup  //WG for streamers, used to ensure that all streamers stopped working
	offsetFlushInterval time.Duration   //Parsed (from OffsetFlush) offset flush interval
	offsetData          chan offsetData //Non-buffered channel to send offset value from streamer to offsetFlusher
	offsetDone          chan bool       //Channel for signalling offsetFlusher go routine to stop
}

func (d *DockerCNTLogs) Description() string {
	return "Read logs from docker containers via Docker API"
}

func (d *DockerCNTLogs) SampleConfig() string { return sampleConfig }

func (d *DockerCNTLogs) Init() error {
	var err error
	var tlsConfig *tls.Config

	//FIXME: Move default values to input creation
	if d.Timeout.Duration == time.Duration(0) {
		d.Timeout.Duration = 5 * time.Second
	}

	if d.client == nil { //Can be already set to mock docker client
		if d.Endpoint == "ENV" {
			d.client, err = docker.NewClientWithOpts(docker.FromEnv)
		} else {
			tlsConfig, err = d.ClientConfig.TLSConfig()
			if err != nil {
				return err
			}

			transport := &http.Transport{
				TLSClientConfig: tlsConfig,
			}
			httpClient := &http.Client{Transport: transport}

			d.client, err = docker.NewClientWithOpts(
				docker.WithHTTPHeaders(defaultHeaders),
				docker.WithHTTPClient(httpClient),
				docker.WithVersion(version),
				docker.WithHost(d.Endpoint))
		}

		if err != nil {
			return err
		}
	}

	//Static container list is mutually exclusive with the
	// container_name_include  & container_name_exclude options!
	// The function below checks:
	// 1. The ambiguity with settings,
	// 2. Every container in static list: for existence and for settings ambiguity
	// If settings is clear, then items from static container list added to filters
	err = d.processStaticContainersList()
	if err != nil {
		return err
	}

	// Create filters
	err = d.createLabelFilters()
	if err != nil {
		return err
	}

	err = d.createContainerFilters()
	if err != nil {
		return err
	}

	err = d.createContainerStateFilters()
	if err != nil {
		return err
	}

	filterArgs := filters.NewArgs()
	for _, state := range containerStates {
		if d.stateFilter.Match(state) {
			filterArgs.Add("status", state)
		}
	}

	if filterArgs.Len() != 0 {
		d.opts = types.ContainerListOptions{
			Filters: filterArgs,
		}
	}

	if d.LogGatherInterval.Duration == time.Duration(0) {
		d.LogGatherInterval.Duration = defaultLogGatherInterval
	}

	d.containerList = make(map[string]context.CancelFunc)
	d.processedContainerList = make(map[string]interface{})

	if rawCommand, ok := d.WhenStaticContainersProcessed["execute_cmd"].([]interface{}); ok && len(rawCommand) > 0 {
		for index, elem := range rawCommand {
			if stringElem, ok := elem.(string); !ok {
				return errors.Errorf("Element '%v', with index '%d' in  'execute_cmd' attribute is not a string!", elem, index)
			} else {
				d.whenProcessedCommand = append(d.whenProcessedCommand, stringElem)
			}
		}

	}

	if d.InitialChunkSize == 0 {
		d.InitialChunkSize = defaultInitialChunkSize
	} else {
		if d.InitialChunkSize <= dockerLogHeaderSize {
			d.InitialChunkSize = 2 * dockerLogHeaderSize
		}
	}

	if d.MaxChunkSize == 0 {
		d.MaxChunkSize = defaultMaxChunkSize
	} else {
		if d.MaxChunkSize <= d.InitialChunkSize {
			d.MaxChunkSize = 5 * d.InitialChunkSize
		}
	}

	//Parsing flush offset
	if d.OffsetFlush == "" {
		d.offsetFlushInterval = defaultFlushInterval
	} else {
		d.offsetFlushInterval, err = time.ParseDuration(d.OffsetFlush)
		if err != nil {
			d.offsetFlushInterval = defaultFlushInterval
			lg.logW("Can't parse '%s' duration, default value will be used.", d.OffsetFlush)
		}
	}

	//Create storage path
	if src, err := os.Stat(d.OffsetStoragePath); os.IsNotExist(err) {
		errDir := os.MkdirAll(d.OffsetStoragePath, 0755)
		if errDir != nil {
			return errors.Errorf("Can't create directory '%s' to store offset, reason: %s", d.OffsetStoragePath, errDir.Error())
		}

	} else if src != nil && src.Mode().IsRegular() {
		return errors.Errorf("'%s' already exist as a file!", d.OffsetStoragePath)
	}

	//Start processed containers checker
	d.processedContainersCheckerDone = make(chan bool)
	d.processedContainersChan = make(chan map[string]interface{})

	d.wg.Add(1)
	go d.checkProcessedContainers(d.processedContainersCheckerDone)

	//Start offset flusher
	d.offsetData = make(chan offsetData)
	d.offsetDone = make(chan bool)
	d.wg.Add(1)
	go d.flushOffset(d.offsetDone)

	return nil
}

func (d *DockerCNTLogs) addToContainerList(containerID string, cancel context.CancelFunc) {
	d.muContainerList.Lock()
	defer d.muContainerList.Unlock()
	d.containerList[containerID] = cancel
}

func (d *DockerCNTLogs) removeFromContainerList(containerID string) {
	d.muContainerList.Lock()
	defer d.muContainerList.Unlock()
	delete(d.containerList, containerID)
}

func (d *DockerCNTLogs) containerInContainerList(containerID string) bool {
	d.muContainerList.Lock()
	defer d.muContainerList.Unlock()
	_, ok := d.containerList[containerID]
	return ok
}

func (d *DockerCNTLogs) cancelStreamers() {
	d.muContainerList.Lock()
	defer d.muContainerList.Unlock()
	for _, cancel := range d.containerList {
		cancel()
	}
}

func (d *DockerCNTLogs) getOffset(offsetFile string) (string, int64) {

	if _, err := os.Stat(offsetFile); !os.IsNotExist(err) {
		data, errRead := ioutil.ReadFile(offsetFile)
		if errRead != nil {
			lg.logE("Error reading offset file '%s', reason: %s", offsetFile, errRead.Error())
		} else {
			timeString := ""
			timeInt, err := strconv.ParseInt(string(data), 10, 64)
			if err == nil {
				timeString = time.Unix(0, timeInt).UTC().Format(time.RFC3339Nano)
			}

			lg.logD("Parsed offset from '%s'\nvalue: %s, %s",
				offsetFile, string(data), timeString)
			return timeString, timeInt
		}
	}
	return "", 0
}

func (d *DockerCNTLogs) matchedContainerName(names []string) bool {
	// Check if all container names are filtered; in practice I believe
	// this array is always of length 1.
	for _, name := range names {
		match := d.containerFilter.Match(strings.TrimPrefix(name, "/"))
		if match {
			return true
		}
	}
	return false
}

func (d *DockerCNTLogs) matchedContainerId(id string) bool {

	return d.containerFilter.Match(id)
}

func (d *DockerCNTLogs) getContainerFromStaticList(id string) map[string]interface{} {

	for _, container := range d.StaticContainerList {
		if container["full_id"].(string) == id {
			return container
		}
	}
	return nil
}

func (d *DockerCNTLogs) getProcessedContainerList() map[string]interface{} {
	var processedContainerList = map[string]interface{}{}
	d.muProcessedContainerList.Lock()
	defer d.muProcessedContainerList.Unlock()

	for k, v := range d.processedContainerList {
		processedContainerList[k] = v
	}

	return processedContainerList
}

func (d *DockerCNTLogs) addToProcessedContainerList(id string) error {

	ctxT, ctxTCancelFunc := context.WithTimeout(context.Background(), d.Timeout.Duration)
	defer ctxTCancelFunc()

	contStatus, err := d.client.ContainerInspect(ctxT, id)
	if err != nil {
		return err
	}

	d.muProcessedContainerList.Lock()
	defer d.muProcessedContainerList.Unlock()

	d.processedContainerList[id] = map[string]interface{}{
		"status":   contStatus.State.Status,
		"started":  contStatus.State.StartedAt,
		"finished": contStatus.State.FinishedAt}
	return nil
}

func (d *DockerCNTLogs) needToProcess(container types.Container, contStatus types.ContainerJSON) bool {
	d.muProcessedContainerList.Lock()
	defer d.muProcessedContainerList.Unlock()

	if elem, ok := d.processedContainerList[container.ID]; ok { //This container was processed earlier
		//Need to check if we need to process it again (in case something changed since last processing

		if elem.(map[string]interface{})["started"].(string) != contStatus.State.StartedAt ||
			elem.(map[string]interface{})["finished"].(string) != contStatus.State.FinishedAt { //Something changed
			return true

		} else { //Nothing changed
			return false
		}
	}

	//This container wasn't processed earlier
	return true
}

func (d *DockerCNTLogs) Gather(acc telegraf.Accumulator) error {
	var err error
	var ctxT context.Context
	var ctxCStream context.Context
	var ctxTCancelFunc context.CancelFunc
	var ctxCCancelFunc context.CancelFunc
	var containers []types.Container
	var contStatus types.ContainerJSON

	//THIS LINE BELOW LEADS TO DATARACE
	//acc.SetPrecision(time.Nanosecond)

	//This timeout context is passed to all docker API requests except the one which return io.ReadCloser with log stream.
	ctxT, ctxTCancelFunc = context.WithTimeout(context.Background(), d.Timeout.Duration)
	defer ctxTCancelFunc()

	//Getting containers list filtered by statuses.
	//Filtering containers based on name/id is preformed later when cycling over the array,
	//as ContainerList not support the filtering based on name/id
	containers, err = d.client.ContainerList(ctxT, d.opts)
	if err != nil {
		return err
	}

	for _, container := range containers {

		//Container is already in the list, skip
		if d.containerInContainerList(container.ID) {
			continue
		}

		//Filtering containers based on name/id
		if !d.matchedContainerName(container.Names) && !d.matchedContainerId(container.ID) {
			continue
		}

		contStatus, err = d.client.ContainerInspect(ctxT, container.ID)
		if err != nil {
			return err
		}

		//Check if we process this container earlier
		//Here filtering containers based on status & start/finish time
		//This needs to be here to cover the cases:
		//1. When the container is exited and we already delivered logs from it (not to deliver it second time)
		//2. When container exited, then again started
		if !d.needToProcess(container, contStatus) {
			lg.logD("Container '%s' logs are already delivered, skipped...", trimId(container.ID))
			continue
		}

		//Kind of a trace, should be disabled in future
		if contStatus.State.Status == "removing" ||
			contStatus.State.Status == "exited" || contStatus.State.Status == "dead" {
			lg.logW("Container '%s' is not running!", trimId(container.ID))
		}

		ctxCStream, ctxCCancelFunc = context.WithCancel(context.Background())
		d.addToContainerList(container.ID, ctxCCancelFunc)

		// Start a new goroutine for every new container that has logs to collect
		d.wgStreamers.Add(1)
		go func(ctxStream context.Context, container types.Container, contStatus types.ContainerJSON) {
			var logStreamer *streamer
			var err error

			defer func() {
				d.addToProcessedContainerList(contStatus.ID)
				//Send updated container list in a blocking mode
				d.processedContainersChan <- d.getProcessedContainerList()

				if logStreamer != nil { //If streamer initialization is not failed
					//Send last offset in a blocking mode
					d.offsetData <- offsetData{contStatus.ID, logStreamer.currentOffset}
				}
				d.removeFromContainerList(contStatus.ID)
				d.wgStreamers.Done()
			}()
			logStreamer, err = newStreamer(ctxStream, container, contStatus, acc, d)
			if err != nil {
				acc.AddError(err)
				return
			}

			err = logStreamer.stream()
			if err != nil && err != context.Canceled && err != io.EOF {
				acc.AddError(err)
				return
			}

		}(ctxCStream, container, contStatus)
	}
	return nil
}

func (d *DockerCNTLogs) Start(acc telegraf.Accumulator) error {
	return nil
}

func (d *DockerCNTLogs) checkProcessedContainers(done <-chan bool) {
	var ticker = time.NewTicker(3 * time.Second)
	var processedContainers = map[string]interface{}{}
	var cmd = &exec.Cmd{}

	defer ticker.Stop()
	defer d.wg.Done()

	for {
		select {
		case <-done:
			return
		case processedContainers = <-d.processedContainersChan:
			lg.logD("checkProcessedContainer: Received processed container list, len: %d", len(processedContainers))
		case <-ticker.C:
			staticContProcessedCount := 0

			for _, staticContainer := range d.StaticContainerList {
				if _, ok := processedContainers[staticContainer["full_id"].(string)]; ok {
					staticContProcessedCount++
				}
			}

			if staticContProcessedCount == len(d.StaticContainerList) && staticContProcessedCount > 0 {
				lg.logI("All static containers are processed!")
				if len(d.whenProcessedCommand) > 0 {
					ctxT, cancel := context.WithTimeout(context.Background(), 10*time.Second)
					lg.logI("Executing '%s' with default 10s timeout...", strings.Join(d.whenProcessedCommand, " "))
					if len(d.whenProcessedCommand) == 1 {
						cmd = exec.CommandContext(ctxT, d.whenProcessedCommand[0])
					} else {
						cmd = exec.CommandContext(ctxT, d.whenProcessedCommand[0], d.whenProcessedCommand[1:]...)
					}

					if err := cmd.Wait(); err != nil && err.Error() != "exec: not started" {
						lg.logW("Error while executing '%s': %v", strings.Join(d.whenProcessedCommand, " "), err)
					}

					stdoutStderr, err := cmd.CombinedOutput()
					if err != nil {
						lg.logW("Error while executing '%s': %v", strings.Join(d.whenProcessedCommand, " "), err)
					} else {
						lg.logI("Output of '%s': %s", strings.Join(d.whenProcessedCommand, " "), stdoutStderr)
					}
					cancel()

				}
			}
		}
	}
}

func (d *DockerCNTLogs) flushOffset(done <-chan bool) {
	var containerOffsets = map[string]int64{}
	var ticker = time.NewTicker(d.offsetFlushInterval)

	defer ticker.Stop()
	defer d.wg.Done()

	for {
		select {
		case <-done:
			return
		case offset := <-d.offsetData:
			containerOffsets[offset.contID] = offset.offset
		case <-ticker.C:
			//Saving offset
			for contID, offsetInt := range containerOffsets {
				filename := path.Join(d.OffsetStoragePath, contID)
				offset := []byte(strconv.FormatInt(offsetInt, 10))

				err := ioutil.WriteFile(filename, offset, 0777)
				if err != nil {
					lg.logE("Can't write streamer offset to file '%s', reason: %v", filename, err)
				} else {
					//Erase flushed data
					delete(containerOffsets, contID)
				}
			}

		}
	}
}

func (d *DockerCNTLogs) Stop() {

	//Stop log streamers
	lg.logD("Shutting down streamers...")
	d.cancelStreamers()
	//wait for streamers to send all data
	d.wgStreamers.Wait()

	lg.logD("Shutting down processed containers checker...")
	//Stop check streamers status
	close(d.processedContainersCheckerDone)

	//Stop offset flushing
	lg.logD("Waiting for shutting down offset flusher...")
	time.Sleep(d.offsetFlushInterval) //This sleep needed to guarantee that offset will be flushed
	close(d.offsetDone)

	//Wait for all go routines to complete
	d.wg.Wait()

	if d.client != nil {
		if err := d.client.Close(); err != nil {
			lg.logD("Can't close docker client, reason: %v", err)
		}
	}
}

func (d *DockerCNTLogs) processStaticContainersList() error {

	//Ambiguity:
	//Static container list is mutually exclusive with the
	// container_name_include  & container_name_exclude options!
	if len(d.StaticContainerList) > 0 &&
		(len(d.ContainerInclude) > 0 || len(d.ContainerExclude) > 0) {

		return errors.Errorf("Settings ambiguity: static container list ([[%s.container]] section) "+
			"is mutually exclusive with the 'container_name_include' & 'container_name_exclude options'",
			inputTitle)
	}

	ctxT, ctxTCancelFunc := context.WithTimeout(context.Background(), d.Timeout.Duration)
	defer ctxTCancelFunc()

	for _, cont := range d.StaticContainerList {
		contID, _ := cont["id"].(string)
		contName, _ := cont["name"].(string)

		//checking if both id and name specified - this is ambiguitiy
		if contName != "" && contID != "" {
			return fmt.Errorf("For static container list %v, both name (%s) and id (%s) specified!", cont, contName, contID)
		}
		identity := ""
		if contID != "" {
			identity = contID
		}
		if contName != "" {
			identity = contName
		}

		//checking container existence
		contStatus, err := d.client.ContainerInspect(ctxT, identity)
		if err != nil {
			return errors.Errorf("Container '%s' from '[[%s.container]]' section is not found,\n"+
				"docker API response: %s",
				identity, inputTitle, err.Error())
		}

		//Enrich static container list with full container ID
		cont["full_id"] = contStatus.ID

		//Append full id to filter
		d.ContainerInclude = append(d.ContainerInclude, contStatus.ID)
	}
	return nil
}

// Following few functions have been inherited from telegraf docker input plugin
func (d *DockerCNTLogs) createContainerFilters() error {
	filter, err := filter.NewIncludeExcludeFilter(d.ContainerInclude, d.ContainerExclude)
	if err != nil {
		return err
	}
	d.containerFilter = filter
	return nil
}

func (d *DockerCNTLogs) createLabelFilters() error {
	filter, err := filter.NewIncludeExcludeFilter(d.LabelInclude, d.LabelExclude)
	if err != nil {
		return err
	}
	d.labelFilter = filter
	return nil
}

func (d *DockerCNTLogs) createContainerStateFilters() error {
	if len(d.ContainerStateInclude) == 0 && len(d.ContainerStateExclude) == 0 {
		//d.ContainerStateInclude = []string{"running"}
		d.ContainerStateInclude = containerStates //all statuses
	}
	filter, err := filter.NewIncludeExcludeFilter(d.ContainerStateInclude, d.ContainerStateExclude)
	if err != nil {
		return err
	}
	d.stateFilter = filter
	return nil
}

func init() {
	inputs.Add("docker_cnt_logs", func() telegraf.Input { return &DockerCNTLogs{} })
}
