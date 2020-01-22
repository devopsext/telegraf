package docker_cnt_logs

import (
	"context"
	"crypto/tls"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"path"
	"runtime"
	"strconv"
	"sync"
	"time"

	"github.com/docker/docker/api/types"
	docker "github.com/docker/docker/client"
	"github.com/influxdata/telegraf"
	tlsint "github.com/influxdata/telegraf/internal/tls"
	"github.com/influxdata/telegraf/plugins/inputs"
	"github.com/pkg/errors"
)

//Docker client wrapper
type Client interface {
	ContainerInspect(ctx context.Context, contID string) (types.ContainerJSON, error)
	ContainerLogs(ctx context.Context, contID string, options types.ContainerLogsOptions) (io.ReadCloser, error)
	Close() error
}

// DockerCNTLogs object
type DockerCNTLogs struct {
	//Passed from config
	Endpoint            string `toml:"endpoint"`
	tlsint.ClientConfig        //Parsing is handled in tlsint module

	InitialChunkSize  int                      `toml:"initial_chunk_size"`
	MaxChunkSize      int                      `toml:"max_chunk_size"`
	OffsetFlush       string                   `toml:"offset_flush"`
	OffsetStoragePath string                   `toml:"offset_storage_path"`
	ShutDownWhenEOF   bool                     `toml:"shutdown_when_eof"`
	TargetContainers  []map[string]interface{} `toml:"container"`

	//Internal
	context context.Context
	//client      *docker.Client
	client      Client
	wg          sync.WaitGroup
	checkerDone chan bool

	offsetDone                 chan bool
	logStreamer                map[string]*streamer //Log streamer
	offsetFlushInterval        time.Duration
	disableTimeStampsStreaming bool //Used for simulating reading logs with or without TS (used in tests only)
	logger                     *loggerWrapper
}

const (
	inputTitle              = "inputs.docker_cnt_logs"
	defaultInitialChunkSize = 1000
	defaultMaxChunkSize     = 5000
	dockerLogHeaderSize     = 8
	dockerTimeStampLength   = 30
	defaultPolingIntervalNS = 500 * time.Millisecond
	defaultFlushInterval    = 3 * time.Second

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

  ## Settings per container (specify as many sections as needed)
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
	version        = "1.21" // Support as old version as possible
	defaultHeaders = map[string]string{"User-Agent": "engine-api-cli-1.0"}
)

func (dl *DockerCNTLogs) Description() string {
	return "Read logs from docker containers via Docker API"
}

func (dl *DockerCNTLogs) SampleConfig() string { return sampleConfig }

func (dl *DockerCNTLogs) Gather(acc telegraf.Accumulator) error {
	return nil
}

func (dl *DockerCNTLogs) Start(acc telegraf.Accumulator) error {
	var err error
	var tlsConfig *tls.Config

	dl.context = context.Background()
	dl.logger = newLoggerWrapper(inputTitle)
	switch dl.Endpoint {
	case "ENV":
		{
			dl.client, err = docker.NewClientWithOpts(docker.FromEnv)
		}
	case "MOCK":
		{
			dl.logger.logW("Starting with mock docker client...")
		}
	default:
		{
			tlsConfig, err = dl.ClientConfig.TLSConfig()
			if err != nil {
				return err
			}

			transport := &http.Transport{
				TLSClientConfig: tlsConfig,
			}
			httpClient := &http.Client{Transport: transport}

			dl.client, err = docker.NewClientWithOpts(
				docker.WithHTTPHeaders(defaultHeaders),
				docker.WithHTTPClient(httpClient),
				docker.WithVersion(version),
				docker.WithHost(dl.Endpoint))
		}
	}

	if err != nil {
		return err
	}

	if dl.InitialChunkSize == 0 {
		dl.InitialChunkSize = defaultInitialChunkSize
	} else {
		if dl.InitialChunkSize <= dockerLogHeaderSize {
			dl.InitialChunkSize = 2 * dockerLogHeaderSize
		}
	}

	if dl.MaxChunkSize == 0 {
		dl.MaxChunkSize = defaultMaxChunkSize
	} else {
		if dl.MaxChunkSize <= dl.InitialChunkSize {
			dl.MaxChunkSize = 5 * dl.InitialChunkSize
		}
	}

	//Parsing flush offset
	if dl.OffsetFlush == "" {
		dl.offsetFlushInterval = defaultFlushInterval
	} else {
		dl.offsetFlushInterval, err = time.ParseDuration(dl.OffsetFlush)
		if err != nil {
			dl.offsetFlushInterval = defaultFlushInterval
			dl.logger.logW("Can't parse '%s' duration, default value will be used.", dl.OffsetFlush)
		}
	}

	//Create storage path
	if src, err := os.Stat(dl.OffsetStoragePath); os.IsNotExist(err) {
		errDir := os.MkdirAll(dl.OffsetStoragePath, 0755)
		if errDir != nil {
			return errors.Errorf("Can't create directory '%s' to store offset, reason: %s", dl.OffsetStoragePath, errDir.Error())
		}

	} else if src != nil && src.Mode().IsRegular() {
		return errors.Errorf("'%s' already exist as a file!", dl.OffsetStoragePath)
	}

	//Prepare data for running log streaming from containers
	dl.logStreamer = map[string]*streamer{}

	for _, container := range dl.TargetContainers {
		streamer, err := newStreamer(&acc, dl, container)
		if err != nil {
			return err
		}
		//Store
		dl.logStreamer[container["id"].(string)] = streamer
	}

	//Starting log streaming (only after full initialization of logger settings performed)
	for _, streamer := range dl.logStreamer {
		go streamer.stream(streamer.done)
	}

	//Start checker
	dl.checkerDone = make(chan bool)
	go dl.checkStreamersStatus(dl.checkerDone)

	//Start offset flusher
	dl.offsetDone = make(chan bool)
	go dl.flushOffset(dl.offsetDone)

	return nil
}

func (dl *DockerCNTLogs) shutdownTelegraf() {
	var err error
	var p *os.Process
	p, err = os.FindProcess(os.Getpid())
	if err != nil {
		dl.logger.logE("Can't get current process PID "+
			"to initiate graceful shutdown: %v.\nHave to panic for shutdown...", err)
	} else {
		if runtime.GOOS == "windows" {
			err = p.Signal(os.Kill) //Interrupt is not supported on windows
		} else {
			err = p.Signal(os.Interrupt)
		}
		if err != nil {
			dl.logger.logW("Can't send signal to main process "+
				"for initiating Telegraf shutdown, reason: %v\nHave to panic for shutdown...", err)
		} else {
			return
		}
	}

	panic(errors.New("Graceful shutdown is not possible, force panic."))
}

func (dl *DockerCNTLogs) checkStreamersStatus(done <-chan bool) {

	dl.wg.Add(1)
	defer dl.wg.Done()

	for {
		select {
		case <-done:
			return
		default:
			closed := 0
			for _, streamer := range dl.logStreamer {

				streamer.lock.Lock()
				if streamer.eofReceived {
					closed++
				}
				streamer.lock.Unlock()
			}
			if closed == len(dl.logStreamer) {
				dl.logger.logI("All target containers are stopped/killed!")
				if dl.ShutDownWhenEOF {
					dl.logger.logI("Telegraf shutdown is requested...")
					dl.shutdownTelegraf()
					return
				}
			}
		}
		time.Sleep(3 * time.Second)
	}
}

func (dl *DockerCNTLogs) flushOffset(done <-chan bool) {

	dl.wg.Add(1)
	defer dl.wg.Done()

	for {
		select {
		case <-done:
			return
		default:

			for _, streamer := range dl.logStreamer {
				filename := path.Join(dl.OffsetStoragePath, streamer.contID)

				//offset := []byte(strconv.FormatInt(atomic.LoadInt64(&logReader.currentOffset), 10))
				streamer.offsetLock.Lock()
				offsetInt := streamer.currentOffset
				streamer.offsetLock.Unlock()
				offset := []byte(strconv.FormatInt(offsetInt, 10))

				err := ioutil.WriteFile(filename, offset, 0777)
				if err != nil {
					dl.logger.logE("Can't write logger offset to file '%s', reason: %v",
						filename, err)
				}
			}
		}
		time.Sleep(dl.offsetFlushInterval)
	}
}

func (dl *DockerCNTLogs) Stop() {

	dl.logger.logD("Shutting down streams checkers...")

	//Stop check streamers status
	close(dl.checkerDone)

	//Stop log streaming
	dl.logger.logD("Shutting down log streamers & closing docker streams...")
	for _, streamer := range dl.logStreamer {
		close(streamer.done) //Signaling go routine to close
		//Unblock goroutine if it waits for the data from stream
		if streamer.contStream != nil {
			if err := streamer.contStream.Close(); err != nil {
				dl.logger.logD("Can't close container logs stream, reason: %v", err)
			}

		}
	}

	//Stop offset flushing
	dl.logger.logD("Waiting for shutting down offset flusher...")
	time.Sleep(dl.offsetFlushInterval) //This sleep needed to guarantee that offset will be flushed
	close(dl.offsetDone)

	//Wait for all go routines to complete
	dl.wg.Wait()

	if dl.client != nil {
		if err := dl.client.Close(); err != nil {
			dl.logger.logD("Can't close docker client, reason: %v", err)
		}
	}

}

func init() {
	inputs.Add("docker_cnt_logs", func() telegraf.Input { return &DockerCNTLogs{} })
}
