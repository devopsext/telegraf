package docker_cnt_logs

import (
	"bufio"
	"context"
	"fmt"
	"github.com/docker/docker/api/types"
	docker "github.com/docker/docker/client"
	"github.com/influxdata/telegraf"
	"github.com/influxdata/telegraf/plugins/inputs"
	"io"
	"sync"
	"time"
)

// DockerCNTLogs object
type DockerCNTLogs struct {
	Endpoint		string			`toml:"endpoint"`
	ContID			string			`toml:"cont_id"`
	context			context.Context
	client			*docker.Client
	contStream		io.ReadCloser
	streamScanner	*bufio.Scanner
	wg				sync.WaitGroup
	acc				telegraf.Accumulator
	stopFlag		chan bool
}

const dockerLogHeaderSize = 8

const sampleConfig = `
#[[inputs.docker_cnt_logs]]  
  ## Docker Endpoint
  ##  To use TCP, set endpoint = "tcp://[ip]:[port]"
  ##  To use environment variables (ie, docker-machine), set endpoint = "ENV"
  #endpoint = "unix:///var/run/docker.sock"

  ## Set container id (long or short from), or container name
  ## to stream logs from 	
  # cont_id = "a7c165cb0930"
`

//Service functions
func IsContainHeader(str *[]byte) (bool) {
	/*
	Docker inject headers when running in detached mode, to distinguish stdout, stderr, etc.
	Header structure:
	header := [8]byte{STREAM_TYPE, 0, 0, 0, SIZE1, SIZE2, SIZE3, SIZE4}
	STREAM_TYPE can be:

	0: stdin (is written on stdout)
	1: stdout
	2: stderr
	SIZE1, SIZE2, SIZE3, SIZE4 are the four bytes of the uint32 size encoded as big endian.

	Following the header is the payload, which is the specified number of bytes of STREAM_TYPE.
	*/
	if len(*str) <= 0 || /*garbage*/
		len(*str) < dockerLogHeaderSize /*No header*/  { return false}

	//Examine first 4 bytes to detect if they match to header structure (see above)
	if ((*str)[0] == 0x0 || (*str)[0] == 0x1 || (*str)[0] == 0x2) &&
		((*str)[1] == 0x0 && (*str)[2] == 0x0 && (*str)[3] == 0x0) {

		return true
	}else{
		return false}
}

//Primary plugin interface

func (dl *DockerCNTLogs) Description() string {
	return "Read logs from docker containers via Docker API"
}

func (dl *DockerCNTLogs) SampleConfig() string { return sampleConfig }

func (dl *DockerCNTLogs) Gather(acc telegraf.Accumulator) error {

	return nil
}

func (dl *DockerCNTLogs) Start(acc telegraf.Accumulator) error {
	var err error

	dl.acc = acc

	dl.context = context.Background()
	if dl.Endpoint == "ENV" {
		dl.client, err = docker.NewClientWithOpts(docker.FromEnv)
	}else {
		dl.client, err = docker.NewClientWithOpts(docker.WithHost(dl.Endpoint))
	}


	if err != nil {
		return err
	}

	options := types.ContainerLogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow: true,
		Timestamps: false,
		Since: fmt.Sprint(int32(time.Now().Unix()))}

	dl.contStream, err = dl.client.ContainerLogs(dl.context, dl.ContID, options)
	if err != nil {
		return err
	}

	dl.streamScanner = bufio.NewScanner(dl.contStream)


	dl.wg.Add(1)
	go dl.LogReceiver()

	return nil
}

func (dl *DockerCNTLogs) Stop() {

	if dl.contStream != nil {
		dl.contStream.Close() //this will cause receiver to get error in .Scan() function and initiate exit process
		dl.contStream = nil
	}

	//Wait for go routine to exit
	dl.wg.Wait()

	//In case we exited based ion stopFlag
	if dl.contStream != nil {
		dl.contStream.Close()
		dl.contStream = nil
	}


	if dl.client !=nil {
		dl.client.Close()
		dl.client = nil
	}
}

func init() {
	inputs.Add("docker_cnt_logs", func() telegraf.Input { return &DockerCNTLogs{stopFlag: make(chan bool)}})
}


func (dl *DockerCNTLogs) LogReceiver() {
	defer dl.wg.Done()
	var outputMsgStartIndex uint

	//Here primary loop
	var field =  make(map[string]interface{})
	var tags = map[string]string{}

	//Reading  1st line and detect if there any header (in non blocking)
	if dl.streamScanner.Scan() {
		firstLine := dl.streamScanner.Bytes()[:] //This is the first line of container output...
		if IsContainHeader(&firstLine) {
			outputMsgStartIndex = dockerLogHeaderSize //Header is in the string, need to strip it out...
		} else {
			outputMsgStartIndex = 0 //No header in the output, start from the 1st letter.
		}
		field["value"] = fmt.Sprintf("%s\n", firstLine[outputMsgStartIndex:])
		dl.acc.AddFields("stream", field, tags)

	} else if err := dl.streamScanner.Err(); err != nil { //Problems while reading... (err == nil if io.EOF is reached)
		dl.acc.AddError(fmt.Errorf("Read error from container '%s': %v", dl.ContID, err))
		return
	}

	//Loop over the rest to print the rest of stream until it ends.
	for dl.streamScanner.Scan() {
		field["value"] = fmt.Sprintf("%s\n", dl.streamScanner.Bytes()[outputMsgStartIndex:])
		dl.acc.AddFields("stream", field, tags)
	}
	if err := dl.streamScanner.Err(); err != nil {

		//select {
		//case <-dl.stopFlag: //Non blocking read from channel
		//	if fmt.Sprintf("%v", err) != "read unix ->docker.sock: use of closed network connection" {
		//		dl.acc.AddError(fmt.Errorf("Read error from container '%s': %v", dl.ContID, err))
		//	}
		//	return
		//default:
		//	dl.acc.AddError(fmt.Errorf("Read error from container '%s': %v", dl.ContID, err))
		//}

		dl.acc.AddError(fmt.Errorf("Read error from container '%s': %v", dl.ContID, err))
	}
	return
}