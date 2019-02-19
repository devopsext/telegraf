package docker_cnt_logs

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"github.com/docker/docker/api/types"
	docker "github.com/docker/docker/client"
	"github.com/influxdata/telegraf"
	//"github.com/influxdata/telegraf/internal/models"
	tlsint "github.com/influxdata/telegraf/internal/tls"
	"github.com/influxdata/telegraf/plugins/inputs"
	"io"
	"net/http"
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

	InitialChunkSize int			`toml:"initial_chunk_size"`
	currentChunkSize int
	MaxChunkSize 	 int			`toml:"max_chunk_size"`
	outputMsgStartIndex uint
	dockerTimeStampLength uint
	buffer []byte
	leftoverBuffer []byte
	length int
	endOfLineIndex int
	quitFlag chan bool
}

const defaultInitialChunkSize = 10000
const defaultMaxChunkSize = 50000

const dockerLogHeaderSize = 8


const sampleConfig = `
#[[inputs.docker_cnt_logs]]  
  
  ## Interval to gather data from docker sock.
  ## the longer the interval the fewer request is made towards docker API (less CPU utilization on dockerd).
  ## On the other hand, this increase the delay between producing logs and delivering it. Reasonable trade off
  ## should be chosen
  # interval = "2000ms"
  
  ## Docker Endpoint
  ##  To use unix, set endpoint = "unix:///var/run/docker.sock" (/var/run/docker.sock is default mount path)
  ##  To use TCP, set endpoint = "tcp://[ip]:[port]"
  ##  To use environment variables (ie, docker-machine), set endpoint = "ENV"
  #endpoint = "unix:///var/run/docker.sock"

  ## Set container id (long or short from), or container name
  ## to stream logs from 	
  # cont_id = "a7c165cb0930"

  ## Set initial chunk size (length of []byte buffer to read from docker socket)
  ## If not set, default value of 'defaultInitialChunkSize = 10000' will be used
  #initial_chunk_size = "10000"	

  ## Set max chunk size (length of []byte buffer to read from docker socket)
  ## If not set, default value of 'defaultMaxChunkSize = 50000' will be used
  ## buffer can grow in capacity adjusting to volume of data received from docker sock 
  #max_chunk_size = "50000"	
`

var (
	version        = "1.21" // 1.24 is when server first started returning its version
	defaultHeaders = map[string]string{"User-Agent": "engine-api-cli-1.0"}
)

//Service functions
func IsContainHeader(str *[]byte, length int ) (bool) {
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
	if length <= 0 || /*garbage*/
		length < dockerLogHeaderSize /*No header*/  { return false}

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
	var reader io.Reader
	var timeStamp time.Time
	var err error
	var field =  make(map[string]interface{})
	var tags = map[string]string{}

	dl.wg.Add(1)
	defer dl.wg.Done()

	//Iterative reads by chunks
	// Read returns:
	// 1. Either full buffer (it means that the message either fit to chunkSize or exceed it. To figure out if it exceed we need to check
	// if the buffer ends with "\r\n"
	// 2. Or partially filled buffer. In this case the rest of the buffer is '\0'

	if len(dl.leftoverBuffer) >0{ //append leftover from previous iteration
		oldLength := dl.length
		dl.buffer = append(dl.leftoverBuffer,dl.buffer...)
		dl.length = oldLength + len(dl.leftoverBuffer)
		//Erasing leftover buffer once used:
		dl.leftoverBuffer = nil
	}

	if dl.length !=0 {
		//Docker API fills buffer with '\0' until the end even if there is no data at all,
		//In this case, dl.length == 0 as it shows the amount of actually read data, but len(dl.buffer) will be equal to cap(dl.buffer),
		// as the buffer will be filled out with '\0'
		dl.endOfLineIndex = dl.length - 1
	}else{
		dl.endOfLineIndex = 0
	}

	//1st case
	if dl.length == len(dl.buffer) && dl.length > 0 {
		//Seek last line end (from the end)
		for ; dl.endOfLineIndex >= 0; dl.endOfLineIndex-- {
			if dl.buffer[dl.endOfLineIndex] == '\n' /*THIS SHOULD BE REVISED*/ {
				if dl.endOfLineIndex != dl.length -1{
					dl.leftoverBuffer = nil
					dl.leftoverBuffer = make ([]byte, (dl.length -1) - dl.endOfLineIndex)
					copy(dl.leftoverBuffer, dl.buffer[dl.endOfLineIndex+1:])
				}
				break}
		}

		//Check if line end is not found
		if dl.endOfLineIndex == -1 {
			//Read next chunk and append to initial buffer (this is expensive operation)
			tempBuffer := make([]byte, dl.currentChunkSize)
			tempLength,err := dl.contStream.Read(tempBuffer)
			if err != nil {
				acc.AddError(fmt.Errorf("Read error from container '%s': %v", dl.ContID, err))
				return err
			}

			dl.buffer = append(dl.buffer,tempBuffer[:tempLength]...)
			dl.length = dl.length + tempLength
			if len(dl.leftoverBuffer) > 0 {
				dl.leftoverBuffer = nil
			}
			//Grow chunk size
			if dl.currentChunkSize*2 < dl.MaxChunkSize{
				dl.currentChunkSize = dl.currentChunkSize*2
			}

			return nil
		}
	}
	//2nd case is addressed by definition of endOfLineIndex

	//Reading the buffer
	//Since read from API can return dl.length==0, and err==nil, we need to additionally check the boundaries
	if (len(dl.buffer) > 0 ) {

		reader = bytes.NewReader(dl.buffer[:dl.endOfLineIndex])
		s := bufio.NewScanner(reader)
		for s.Scan() {
			//fmt.Println(s.Bytes())
			totalLineLength := len(s.Bytes())
			if uint(totalLineLength) < dl.outputMsgStartIndex + dl.dockerTimeStampLength + 1 { //no time stamp
				timeStamp = time.Now()
				field["value"] = fmt.Sprintf("%s\n", s.Bytes()[dl.outputMsgStartIndex:])

			}else{
				field["value"] = fmt.Sprintf("%s\n", s.Bytes()[dl.outputMsgStartIndex + dl.dockerTimeStampLength:])
				timeStamp,err = time.Parse(time.RFC3339Nano,fmt.Sprintf("%s",s.Bytes()[dl.outputMsgStartIndex:dl.dockerTimeStampLength]))
				if err != nil{
					acc.AddError(fmt.Errorf("Can't parse time stamp from string, comtainer '%s': %v", dl.ContID, err))
					timeStamp = time.Now()
				}
			}
			//fmt.Printf("%s\n",s.Bytes()[dl.outputMsgStartIndex:dl.dockerTimeStampLength])
			dl.acc.AddFields("stream", field, tags,timeStamp)

		}


		//Control the size of buffer
		if len (dl.buffer) > dl.MaxChunkSize {
			dl.buffer = nil
			dl.buffer = make([]byte,dl.currentChunkSize)
		}
	}

	//Read next chunk
	dl.length,err = dl.contStream.Read(dl.buffer) //Can be a case when API returns dl.length==0, and err==nil

	if err != nil {
		if err.Error() == "EOF" {
			err = fmt.Errorf("Can't read from container '%s'. Stream closed unexpectedly, 'EOF' received, looks like container stopped...", dl.ContID)
			if dl.contStream != nil {
				dl.contStream.Close()
				dl.contStream = nil
			}

			if dl.client !=nil {
				dl.client.Close()
				dl.client = nil
			}

			acc.AddError(err)
			panic(err)

		}else {
			acc.AddError(fmt.Errorf("Read error from container '%s': %v", dl.ContID, err))
			return err
		}
	}
	//runtime.GC()

	return nil
}

func (dl *DockerCNTLogs) Start(acc telegraf.Accumulator) error {
	var err error

	dl.acc = acc

	dl.context = context.Background()
	if dl.Endpoint == "ENV" {
		dl.client, err = docker.NewClientWithOpts(docker.FromEnv)
	}else{

		var cc tlsint.ClientConfig
		tlsConfig, err := cc.TLSConfig()
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

	if err != nil {
		return err
	}

	if dl.InitialChunkSize == 0 {dl.InitialChunkSize = defaultInitialChunkSize}
	if dl.MaxChunkSize == 0 {dl.MaxChunkSize = defaultMaxChunkSize}

	dl.currentChunkSize = dl.InitialChunkSize
	dl.dockerTimeStampLength = 30

	options := types.ContainerLogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow: true,
		Timestamps: true,
		Since: fmt.Sprint(int32(time.Now().Unix()))}

	dl.contStream, err = dl.client.ContainerLogs(dl.context, dl.ContID, options)
	if err != nil {
		return err
	}

	dl.quitFlag = make (chan bool)

	dl.buffer = make([]byte, dl.InitialChunkSize)
	dl.length,err = dl.contStream.Read(dl.buffer)

	if err != nil {
		acc.AddError(fmt.Errorf("Read error from container '%s': %v", dl.ContID, err))
		dl.Stop()
		return err
	}

	if IsContainHeader(&dl.buffer,dl.length) {
		dl.outputMsgStartIndex = dockerLogHeaderSize //Header is in the string, need to strip it out...
	} else {
		dl.outputMsgStartIndex = 0 //No header in the output, start from the 1st letter.
	}
	dl.endOfLineIndex = dl.length - 1

	return nil
}

func (dl *DockerCNTLogs) Stop() {

	//Wait for Gather to complete
	dl.quitFlag <- true
	dl.wg.Wait()

	if dl.contStream != nil {
		dl.contStream.Close() //this will cause receiver to get error in .Scan() function and initiate exit process
		dl.contStream = nil
	}

	if dl.client !=nil {
		dl.client.Close()
		dl.client = nil
	}
}

func init() {
	inputs.Add("docker_cnt_logs", func() telegraf.Input { return &DockerCNTLogs{}})
}
