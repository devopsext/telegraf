package docker_cnt_logs

import (
	"context"
	"encoding/binary"
	"fmt"
	"github.com/docker/docker/api/types"
	docker "github.com/docker/docker/client"
	"github.com/influxdata/telegraf"
	"github.com/pkg/errors"
	"io/ioutil"
	"log"
	"os"
	"path"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"

	tlsint "github.com/influxdata/telegraf/internal/tls"
	"github.com/influxdata/telegraf/plugins/inputs"
	"io"
	"net/http"
	"sync"
	"time"
)

// DockerCNTLogs object
type DockerCNTLogs struct {
	//Passed from config
	Endpoint      		string 						`toml:"endpoint"`
	tlsint.ClientConfig //Parsing is handled in tlsint module

	InitialChunkSize	int 						`toml:"initial_chunk_size"`
	MaxChunkSize		int							`toml:"max_chunk_size"`
	OffsetFlush			string						`toml:"offset_flush"`
	OffsetStoragePath	string						`toml:"offset_storage_path"`
	ShutDownWhenEOF		bool						`toml:"shutdown_when_eof"`
	TargetContainers	[]map[string]interface{} 	`toml:"container"`

	//Internal
	context       		context.Context
	client        		*docker.Client
	wg            		sync.WaitGroup
	checkerQuitFlag     chan bool
	offsetQuitFlag		chan bool
	logReader			map[string]*logReader //Log reader data...
	offsetFlushInterval	time.Duration
}

type logReader struct {
	contID        string
	contStream    io.ReadCloser

	msgHeaderExamined     bool
	dockerTimeStamps      bool
	interval			  time.Duration
	initialChunkSize      int
	currentChunkSize      int
	maxChunkSize          int
	outputMsgStartIndex   uint
	dockerTimeStampLength uint
	buffer                []byte
	leftoverBuffer        []byte
	length                int
	endOfLineIndex        int
	tags				  map[string]string
	quitFlag              chan bool
	eofReceived			  int32
	currentOffset		  int64

}

const defaultInitialChunkSize = 10000
const defaultMaxChunkSize = 50000

const dockerLogHeaderSize = 8
const dockerTimeStampLength = 30

const defaultPolingIntervalNS = 500*time.Millisecond

const defaultFlushInterval = 3 *time.Second

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

  ## Optional TLS Config
  # tls_ca = "/etc/telegraf/ca.pem"
  # tls_cert = "/etc/telegraf/cert.pem"
  # tls_key = "/etc/telegraf/key.pem"

  ## Use TLS but skip chain & host verification
  # insecure_skip_verify = false

  #############################################################################
  # Log streaming settings

  # Set initial chunk size (length of []byte buffer to read from docker socket)
  # If not set, default value of 'defaultInitialChunkSize = 10000' will be used
  initial_chunk_size = 10000 # 10K symbols

  # Set max chunk size (length of []byte buffer to read from docker socket)
  # If not set, default value of 'defaultMaxChunkSize = 50000' will be used
  # buffer can grow in capacity adjusting to volume of data received from docker sock
  max_chunk_size = 50000 # 50K symbols

  # Offset flush interval. How often the offset pointer (see below) in the
  # log stream is flashed to file.Offset pointer represents the unix time stamp
  # for last message read from log stream (default - 3 sec)
  # offset_flush = "3s"

  # Offset storage path (mandatory)
  #offset_storage_path = "/var/run/collector_offset"

  # Shutdown telegraf if all log streaming containers stopped/killed, default - false
  # shutdown_when_eof = false

  #Settings per container (specify as many sections as needed)
  #[[inputs.docker_cnt_logs.container]]
    # Set container id (long or short from), or container name
    # to stream logs from, this attribute is mandatory
    #id = "dc23d3ea534b3a6ec3934ae21e2dd4955fdbf61106b32fa19b831a6040a7feef"

    ## Override common settings
    ## input interval (specified or inherited from agent section)
    #interval = "500ms"

    ## Initial chunk size
    # initial_chunk_size = 20000 # 2K symbols

    ## Max chunk size
    # max_chunk_size = 60000 # 6K symbols

    #Set additional tags that will be tagged to the stream from the current container.
    #tags = [
    #    "tag1=value1",
    #    "tag2=value2"
    #]

  #[[inputs.docker_cnt_logs.container]]
  #  id = "009d82030745c9994e2f5c2280571e8b9f95681793a8f7073210759c74c1ea36"
  #  interval = "600ms"


`

//TODO: Check what version to support!!!
var (
	version        = "1.21" // Support as old version as possible
	defaultHeaders = map[string]string{"User-Agent": "engine-api-cli-1.0"}
)

//Service functions
func isContainsHeader(str *[]byte, length int) bool {

	//Docker inject headers when running in detached mode, to distinguish stdout, stderr, etc.
	//Header structure:
	//header := [8]byte{STREAM_TYPE, 0, 0, 0, SIZE1, SIZE2, SIZE3, SIZE4}
	//STREAM_TYPE can be:
	//
	//0: stdin (is written on stdout)
	//1: stdout
	//2: stderr
	//SIZE1, SIZE2, SIZE3, SIZE4 are the four bytes of the uint32 size encoded as big endian.
	//
	//Following the header is the payload, which is the specified number of bytes of STREAM_TYPE.

	var result bool
	if length <= 0 || /*garbage*/
		length < dockerLogHeaderSize /*No header*/ {
		return false
	}

	log.Printf("D! [inputs.docker_cnt_logs] Raw string for detecting headers:\n%s\n", str)
	log.Printf("D! [inputs.docker_cnt_logs] First 4 bytes: '%v,%v,%v,%v', string representation: '%s'", (*str)[0], (*str)[1], (*str)[2], (*str)[3], (*str)[0:4])
	log.Printf("D! [inputs.docker_cnt_logs] Big endian value: %d", binary.BigEndian.Uint32((*str)[4:dockerLogHeaderSize]))

	//Examine first 4 bytes to detect if they match to header structure (see above)
	if ((*str)[0] == 0x0 || (*str)[0] == 0x1 || (*str)[0] == 0x2) &&
		((*str)[1] == 0x0 && (*str)[2] == 0x0 && (*str)[3] == 0x0) &&
		binary.BigEndian.Uint32((*str)[4:dockerLogHeaderSize]) >= 2 /*Encoding big endian*/ {
		//binary.BigEndian.Uint32((*str)[4:dockerLogHeaderSize]) - calculates message length.
		//Minimum message length with timestamp is 32 (timestamp (30 symbols) + space + '\n' = 32.  But in case you switch timestamp off
		//it will be 2 (space + '\n')

		log.Printf("I! [inputs.docker_cnt_logs] Detected: log messages from docker API streamed WITH headers...")
		result = true

	} else {
		log.Printf("I! [inputs.docker_cnt_logs] Detected: log messages from docker API streamed WITHOUT headers...")

		result = false
	}

	return result
}

//If there is no new line in interval [eolIndex-HeaderSize,eolIndex+HeaderSize],
//then we are definitely not in the middle of header, otherwise, we are.
func isNewLineInMsgHeader(str *[]byte, eolIndex int) bool {
	//Edge case:
	if eolIndex == dockerLogHeaderSize {
		return false
	}

	//frame := (*str)[eolIndex-dockerLogHeaderSize:eolIndex]
	//fmt.Printf("frmae: '%s'",frame)

	//If in the frame there is the following sequence '\n, 0|1|2, 0,0,0',
	// then we are somewhere in the header. First '\n means that there is another
	// srting that ends before this, and we actually need to find this particular '\n'
	for i := eolIndex - dockerLogHeaderSize; i < eolIndex; i++ {
		if ((*str)[i] == '\n') &&
			((*str)[i+1] == 0x1 || (*str)[i+1] == 0x2 || (*str)[i+1] == 0x0) &&
			((*str)[i+2] == 0x0 && (*str)[i+3] == 0x0 && (*str)[i+4] == 0x0) {
			return true
		}
	}

	return false
}

func getInputIntervalDuration(acc telegraf.Accumulator) (dur time.Duration) {
	// I know, pretty ugly code, but we need to be redundant in case the
	// types will changes
	emptyValue := reflect.Value{}
	agentAccumulator := reflect.ValueOf(acc).Elem()
	if agentAccumulator.Kind() == reflect.Struct {
		if agentAccumulator.FieldByName("maker") == emptyValue { //Empty value
			log.Printf("W! [inputs.docker_cnt_logs] Error while parsing agent.accumulator type, filed 'maker'" +
				" is not found.\nDefault pooling duration '%d' nano sec. will be used",defaultPolingIntervalNS)
			dur = defaultPolingIntervalNS
		} else {
			runningInput := reflect.Indirect(agentAccumulator.FieldByName("maker").Elem())
			if reflect.Indirect(runningInput).FieldByName("Config") == emptyValue {
				log.Printf("W! [inputs.docker_cnt_logs] Error while parsing models.RunningInput type, filed 'Config'" +
					" is not found.\nDefault pooling duration '%d' nano sec. will be used",defaultPolingIntervalNS)
				dur = defaultPolingIntervalNS
			}else {
				if reflect.Indirect(reflect.Indirect(runningInput).FieldByName("Config").Elem()).FieldByName("Interval") == emptyValue {
					log.Printf("W! [inputs.docker_cnt_logs] Error while parsing models.InputConfig type, filed 'Interval'" +
						" is not found.\nDefault pooling duration '%d' nano sec. will be used",defaultPolingIntervalNS)
					dur = defaultPolingIntervalNS

				}else{
					interval := reflect.Indirect(reflect.Indirect(runningInput).FieldByName("Config").Elem()).FieldByName("Interval")
					if interval.Kind() == reflect.Int64 {
						dur = time.Duration(interval.Int())
					} else {
						log.Printf("W! [inputs.docker_cnt_logs] Error while parsing models.RunningInput.Interval type, filed " +
							" is not of type 'int'.\nDefault pooling duration '%d' nano sec. will be used",defaultPolingIntervalNS)
						dur = defaultPolingIntervalNS
					}
				}


			}


		}

	}

	return
}

func getOffset(offsetFile string) (string,int64) {

	if _, err := os.Stat(offsetFile); !os.IsNotExist(err) {
		data,errRead := ioutil.ReadFile(offsetFile)
		if errRead != nil {
			log.Printf("E! [inputs.docker_cnt_logs] Error reading offset file '%s', reason: %s",offsetFile,errRead.Error() )
		}else{
			timeString := ""
			timeInt,err := strconv.ParseInt(string(data), 10, 64)
			if err ==nil{
				timeString = time.Unix(timeInt,0).Format(time.RFC3339)
			}

			log.Printf("D! [inputs.docker_cnt_logs] Parsed offset from '%s'\nvalue: %s, %s",offsetFile,string(data),timeString )
			return string(data),timeInt
		}
	}

	return "",0
}


//Primary plugin interface

func (dl *DockerCNTLogs) Description() string {
	return "Read logs from docker containers via Docker API"
}

func (dl *DockerCNTLogs) SampleConfig() string { return sampleConfig }

func (dl *DockerCNTLogs) Gather(acc telegraf.Accumulator) error {

	//var err error
	//var logStreamEOFReceived = false
	//
	//dl.wg.Add(1)
	//defer dl.wg.Done()
	//
	////Iterative reads by chunks
	//// While reading in chunks, there are 2 general cases:
	//// 1. Either full buffer (it means that the message either fit to chunkSize or exceed it. To figure out if it exceed we need to check
	//// if the buffer ends with "\r\n"
	//// 2. Or partially filled buffer. In this case the rest of the buffer is '\0'
	//
	////Read from docker API
	//dl.length, err = dl.contStream.Read(dl.buffer) //Can be a case when API returns dl.length==0, and err==nil
	//
	//if err != nil {
	//	if err.Error() == "EOF" { //Special case, need to flush data and exit
	//		logStreamEOFReceived = true
	//	} else {
	//		acc.AddError(fmt.Errorf("Read error from container '%s': %v", dl.ContID, err))
	//		return err
	//	}
	//}
	//
	//if !dl.msgHeaderExamined {
	//	if IsContainsHeader(&dl.buffer, dl.length) {
	//		dl.outputMsgStartIndex = dockerLogHeaderSize //Header is in the string, need to strip it out...
	//	} else {
	//		dl.outputMsgStartIndex = 0 //No header in the output, start from the 1st letter.
	//	}
	//	dl.msgHeaderExamined = true
	//}
	//
	//
	//if len(dl.leftoverBuffer) > 0 { //append leftover from previous iteration
	//	oldLength := dl.length
	//	dl.buffer = append(dl.leftoverBuffer, dl.buffer...)
	//	dl.length = oldLength + len(dl.leftoverBuffer)
	//	//Erasing leftover buffer once used:
	//	dl.leftoverBuffer = nil
	//}
	//
	//if dl.length != 0 {
	//	//Docker API fills buffer with '\0' until the end even if there is no data at all,
	//	//In this case, dl.length == 0 as it shows the amount of actually read data, but len(dl.buffer) will be equal to cap(dl.buffer),
	//	// as the buffer will be filled out with '\0'
	//	dl.endOfLineIndex = dl.length - 1
	//} else {
	//	dl.endOfLineIndex = 0
	//}
	//
	////1st case
	//if dl.length == len(dl.buffer) && dl.length > 0 {
	//	//Seek last line end (from the end), ignoring the case when this line end is in the message header
	//	//for ; dl.endOfLineIndex >= 0; dl.endOfLineIndex-- {
	//	for ; dl.endOfLineIndex >= int(dl.outputMsgStartIndex); dl.endOfLineIndex-- {
	//		if dl.buffer[dl.endOfLineIndex] == '\n' {
	//
	//			//Skip '\n' if there are headers and '\n' is inside header
	//			if dl.outputMsgStartIndex > 0 && IsNewLineInMsgHeader(&dl.buffer, dl.endOfLineIndex) {
	//				continue
	//			}
	//
	//			if dl.endOfLineIndex != dl.length-1 {
	//				// Moving everything that is after dl.endOfLineIndex to leftover buffer (2nd case)
	//				dl.leftoverBuffer = nil
	//				dl.leftoverBuffer = make([]byte, (dl.length-1)-dl.endOfLineIndex)
	//				copy(dl.leftoverBuffer, dl.buffer[dl.endOfLineIndex+1:])
	//			}
	//			break
	//		}
	//	}
	//
	//	//Check if line end is not found
	//	//if dl.endOfLineIndex == -1 {
	//	if dl.endOfLineIndex == int(dl.outputMsgStartIndex-1) {
	//		//This is 1st case...
	//
	//		//Read next chunk and append to initial buffer (this is expensive operation)
	//		tempBuffer := make([]byte, dl.currentChunkSize)
	//
	//		//This would be non blocking read, as it is reading of something that is for sure in the io stream
	//		tempLength, err := dl.contStream.Read(tempBuffer)
	//		if err != nil { //Here there is no special check for 'EOF'. In this case we will have EOF check on the read above (in next iteration).
	//			acc.AddError(fmt.Errorf("Read error from container '%s': %v", dl.ContID, err))
	//			return err
	//		}
	//
	//		dl.buffer = append(dl.buffer, tempBuffer[:tempLength]...)
	//		dl.length = dl.length + tempLength
	//		if len(dl.leftoverBuffer) > 0 {
	//			dl.leftoverBuffer = nil
	//		}
	//		//Grow chunk size
	//		if dl.currentChunkSize*2 < dl.MaxChunkSize {
	//			dl.currentChunkSize = dl.currentChunkSize * 2
	//		}
	//
	//		return nil
	//	}
	//}
	//
	////Parsing the buffer and passing data to accumulator
	////Since read from API can return dl.length==0, and err==nil, we need to additionally check the boundaries
	//if len(dl.buffer) > 0 && dl.endOfLineIndex > 0 {
	//
	//	totalLineLength := 0
	//	var timeStamp time.Time
	//	var field = make(map[string]interface{})
	//	var tags = map[string]string{}
	//
	//	/*Short alternative*/
	//	for i := 0; i <= dl.endOfLineIndex; i = i + totalLineLength + 1 { // +1 means that we skip '\n'
	//
	//		//Checking boundaries:
	//		if i+int(dl.outputMsgStartIndex) > dl.endOfLineIndex { //sort of garbage
	//			timeStamp = time.Now()
	//			field["value"] = fmt.Sprintf("%s\n", dl.buffer[i:dl.endOfLineIndex])
	//			dl.acc.AddFields("stream", field, tags, timeStamp)
	//			break
	//		}
	//
	//		totalLineLength = 0
	//
	//		//Looking for the end of the line (skipping index):
	//		for j := i + int(dl.outputMsgStartIndex); j <= dl.endOfLineIndex; j++ {
	//			if dl.buffer[j] == '\n' {
	//				totalLineLength = j - i
	//				break
	//			}
	//		}
	//		if totalLineLength == 0 {
	//			totalLineLength = dl.endOfLineIndex - i
	//		}
	//
	//		//Getting stream type (if header persist)
	//		if dl.outputMsgStartIndex > 0 {
	//			if dl.buffer[i] == 0x1 {
	//				tags["stream"] = "stdout"
	//			} else if dl.buffer[i] == 0x2 {
	//				tags["stream"] = "stderr"
	//			} else if dl.buffer[i] == 0x0 {
	//				tags["stream"] = "stdin"
	//			}
	//		} else {
	//			tags["stream"] = "interactive"
	//		}
	//
	//		if uint(totalLineLength) < dl.outputMsgStartIndex+dl.dockerTimeStampLength+1 || !dl.dockerTimeStamps { //no time stamp
	//			timeStamp = time.Now()
	//			field["value"] = fmt.Sprintf("%s\n", dl.buffer[i+int(dl.outputMsgStartIndex):i+totalLineLength])
	//		} else {
	//			timeStamp, err = time.Parse(time.RFC3339Nano, fmt.Sprintf("%s", dl.buffer[i+int(dl.outputMsgStartIndex):i+int(dl.outputMsgStartIndex)+int(dl.dockerTimeStampLength)]))
	//			if err != nil {
	//				acc.AddError(fmt.Errorf("Can't parse time stamp from string, container '%s': %v. Raw message string:\n%s\nOutput msg start index: %d", dl.ContID, err, dl.buffer[i:i+totalLineLength], dl.outputMsgStartIndex))
	//				log.Printf("E! [inputs.docker_cnt_logs]\n=========== buffer[:dl.endOfLineIndex] ===========\n%s\n=========== ====== ===========\n", dl.buffer[:dl.endOfLineIndex])
	//			}
	//			field["value"] = fmt.Sprintf("%s\n", dl.buffer[i+int(dl.outputMsgStartIndex)+int(dl.dockerTimeStampLength)+1:i+totalLineLength])
	//		}
	//		dl.acc.AddFields("stream", field, tags, timeStamp)
	//
	//	}
	//}
	//
	////Control the size of buffer
	//if len(dl.buffer) > dl.MaxChunkSize {
	//	dl.buffer = nil
	//	dl.buffer = make([]byte, dl.currentChunkSize)
	//	runtime.GC()
	//}
	//
	//if logStreamEOFReceived { //Graceful telegraf shutdown
	//	log.Printf("E! [inputs.docker_cnt_logs]: Container '%s': log stream closed, 'EOF' received. Telegraf is requested to shutdown!",dl.ContID)
	//	dl.ShutdownTelegraf()
	//}

	return nil
}

func (dl *DockerCNTLogs) goGather(acc telegraf.Accumulator, lr *logReader) error {

	var err error

	dl.wg.Add(1)
	defer dl.wg.Done()


	for {
		select {
		case <-lr.quitFlag:
			return nil
		default:
			//Iterative reads by chunks
			// While reading in chunks, there are 2 general cases:
			// 1. Either full buffer (it means that the message either fit to chunkSize or exceed it. To figure out if it exceed we need to check
			// if the buffer ends with "\r\n"
			// 2. Or partially filled buffer. In this case the rest of the buffer is '\0'

			//Read from docker API
			lr.length, err = lr.contStream.Read(lr.buffer) //Can be a case when API returns lr.length==0, and err==nil

			if err != nil {
				if err.Error() == "EOF" { //Special case, need to flush data and exit
					atomic.AddInt32(&lr.eofReceived,1)
				} else {
					acc.AddError(fmt.Errorf("Read error from container '%s': %v", lr.contID, err))
					return err
				}
			}

			if !lr.msgHeaderExamined {
				if isContainsHeader(&lr.buffer, lr.length) {
					lr.outputMsgStartIndex = dockerLogHeaderSize //Header is in the string, need to strip it out...
				} else {
					lr.outputMsgStartIndex = 0 //No header in the output, start from the 1st letter.
				}
				lr.msgHeaderExamined = true
			}


			if len(lr.leftoverBuffer) > 0 { //append leftover from previous iteration
				ollrength := lr.length
				lr.buffer = append(lr.leftoverBuffer, lr.buffer...)
				lr.length = ollrength + len(lr.leftoverBuffer)
				//Erasing leftover buffer once used:
				lr.leftoverBuffer = nil
			}

			if lr.length != 0 {
				//Docker API fills buffer with '\0' until the end even if there is no data at all,
				//In this case, lr.length == 0 as it shows the amount of actually read data, but len(lr.buffer) will be equal to cap(lr.buffer),
				// as the buffer will be filled out with '\0'
				lr.endOfLineIndex = lr.length - 1
			} else {
				lr.endOfLineIndex = 0
			}

			//1st case
			if lr.length == len(lr.buffer) && lr.length > 0 {
				//Seek last line end (from the end), ignoring the case when this line end is in the message header
				//for ; lr.endOfLineIndex >= 0; lr.endOfLineIndex-- {
				for ; lr.endOfLineIndex >= int(lr.outputMsgStartIndex); lr.endOfLineIndex-- {
					if lr.buffer[lr.endOfLineIndex] == '\n' {

						//Skip '\n' if there are headers and '\n' is inside header
						if lr.outputMsgStartIndex > 0 && isNewLineInMsgHeader(&lr.buffer, lr.endOfLineIndex) {
							continue
						}

						if lr.endOfLineIndex != lr.length-1 {
							// Moving everything that is after lr.endOfLineIndex to leftover buffer (2nd case)
							lr.leftoverBuffer = nil
							lr.leftoverBuffer = make([]byte, (lr.length-1)-lr.endOfLineIndex)
							copy(lr.leftoverBuffer, lr.buffer[lr.endOfLineIndex+1:])
						}
						break
					}
				}

				//Check if line end is not found
				//if lr.endOfLineIndex == -1 {
				if lr.endOfLineIndex == int(lr.outputMsgStartIndex-1) {
					//This is 1st case...

					//Read next chunk and append to initial buffer (this is expensive operation)
					tempBuffer := make([]byte, lr.currentChunkSize)

					//This would be non blocking read, as it is reading of something that is for sure in the io stream
					tempLength, err := lr.contStream.Read(tempBuffer)
					if err != nil { //Here there is no special check for 'EOF'. In this case we will have EOF check on the read above (in next iteration).
						acc.AddError(fmt.Errorf("Read error from container '%s': %v", lr.contID, err))
						return err
					}

					lr.buffer = append(lr.buffer, tempBuffer[:tempLength]...)
					lr.length = lr.length + tempLength
					if len(lr.leftoverBuffer) > 0 {
						lr.leftoverBuffer = nil
					}
					//Grow chunk size
					if lr.currentChunkSize*2 < lr.maxChunkSize {
						lr.currentChunkSize = lr.currentChunkSize * 2
					}

					return nil
				}
			}

			//Parsing the buffer and passing data to accumulator
			//Since read from API can return lr.length==0, and err==nil, we need to additionally check the boundaries
			if len(lr.buffer) > 0 && lr.endOfLineIndex > 0 {

				totalLineLength := 0
				var timeStamp time.Time
				var field = make(map[string]interface{})
				//var tags = map[string]string{}
				var tags = lr.tags

				/*Short alternative*/
				for i := 0; i <= lr.endOfLineIndex; i = i + totalLineLength + 1 { // +1 means that we skip '\n'

					//Checking boundaries:
					if i+int(lr.outputMsgStartIndex) > lr.endOfLineIndex { //sort of garbage
						timeStamp = time.Now()
						field["value"] = fmt.Sprintf("%s\n", lr.buffer[i:lr.endOfLineIndex])
						acc.AddFields("stream", field, tags, timeStamp)
						break
					}

					totalLineLength = 0

					//Looking for the end of the line (skipping index):
					for j := i + int(lr.outputMsgStartIndex); j <= lr.endOfLineIndex; j++ {
						if lr.buffer[j] == '\n' {
							totalLineLength = j - i
							break
						}
					}
					if totalLineLength == 0 {
						totalLineLength = lr.endOfLineIndex - i
					}

					//Getting stream type (if header persist)
					if lr.outputMsgStartIndex > 0 {
						if lr.buffer[i] == 0x1 {
							tags["stream_type"] = "stdout"
						} else if lr.buffer[i] == 0x2 {
							tags["stream_type"] = "stderr"
						} else if lr.buffer[i] == 0x0 {
							tags["stream_type"] = "stdin"
						}
					} else {
						tags["stream_type"] = "tty"
					}

					if uint(totalLineLength) < lr.outputMsgStartIndex+lr.dockerTimeStampLength+1 || !lr.dockerTimeStamps { //no time stamp
						timeStamp = time.Now()
						field["value"] = fmt.Sprintf("%s\n", lr.buffer[i+int(lr.outputMsgStartIndex):i+totalLineLength])
					} else {
						timeStamp, err = time.Parse(time.RFC3339Nano, fmt.Sprintf("%s", lr.buffer[i+int(lr.outputMsgStartIndex):i+int(lr.outputMsgStartIndex)+int(lr.dockerTimeStampLength)]))
						if err != nil {
							acc.AddError(fmt.Errorf("Can't parse time stamp from string, container '%s': %v. Raw message string:\n%s\nOutput msg start index: %d", lr.contID, err, lr.buffer[i:i+totalLineLength], lr.outputMsgStartIndex))
							log.Printf("E! [inputs.docker_cnt_logs]\n=========== buffer[:lr.endOfLineIndex] ===========\n%s\n=========== ====== ===========\n", lr.buffer[:lr.endOfLineIndex])
						}
						field["value"] = fmt.Sprintf("%s\n", lr.buffer[i+int(lr.outputMsgStartIndex)+int(lr.dockerTimeStampLength)+1:i+totalLineLength])
					}

					acc.AddFields("stream", field, tags, timeStamp)

					//Saving offset
					currentOffset := atomic.LoadInt64(&lr.currentOffset)
					atomic.AddInt64(&lr.currentOffset,timeStamp.Unix() - currentOffset)
				}
			}

			//Control the size of buffer`
			if len(lr.buffer) > lr.maxChunkSize {
				lr.buffer = nil
				lr.buffer = make([]byte, lr.currentChunkSize)
				runtime.GC()
			}

			if atomic.LoadInt32(&lr.eofReceived) > 0 {
				log.Printf("E! [inputs.docker_cnt_logs] container '%s': log stream closed, 'EOF' received.",lr.contID)
				close(lr.quitFlag)
				lr.quitFlag = nil
				return nil
			}

		}

		time.Sleep(lr.interval)
	}

}

func (dl *DockerCNTLogs) Start(acc telegraf.Accumulator) error {
	var err error

	dl.context = context.Background()
	if dl.Endpoint == "ENV" {
		dl.client, err = docker.NewClientWithOpts(docker.FromEnv)
	} else
	{

		tlsConfig, err  := dl.ClientConfig.TLSConfig()
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

	//Default behaviour - stream logs with time-stamps

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
	if dl.OffsetFlush==""{
		dl.offsetFlushInterval = defaultFlushInterval
	}else{
		dl.offsetFlushInterval,err = time.ParseDuration(dl.OffsetFlush)
		if err != nil {
			dl.offsetFlushInterval = defaultFlushInterval
			log.Printf("W! [inputs.docker_cnt_logs] Can't parse '%s' duration, default value will be used.")
		}
	}

	//Create storage path
	if src, err := os.Stat(dl.OffsetStoragePath); os.IsNotExist(err) {
		errDir := os.MkdirAll(dl.OffsetStoragePath, 0755)
		if errDir != nil {
			return errors.Errorf("Can't create directory '%s' to store offset, reason: %s",dl.OffsetStoragePath,errDir.Error())
		}

	}else if src != nil && src.Mode().IsRegular() {
			return errors.Errorf("'%s' already exist as a file!",dl.OffsetStoragePath)
	}

	//Prepare data for running log streaming from containers
	dl.logReader = map[string]*logReader{}

	for _,container := range dl.TargetContainers {

		if _,ok := container["id"]; !ok { //id is not specified
			return errors.Errorf("Mandatory attribute 'id' is not specified for '[[inputs.docker_cnt_logs.container]]' section!")
		}
		logReader := logReader{}
		logReader.contID =  container["id"].(string)


		if _,ok := container["interval"]; ok { //inetrval is specified
			logReader.interval,err = time.ParseDuration(container["interval"].(string))
			if err !=nil {
				return errors.Errorf("Can't parse interval from string '%s', reason: %s",container["interval"].(string),err.Error())
			}
		}else {
			logReader.interval = getInputIntervalDuration(acc)
		}

		logReader.dockerTimeStamps = true
		logReader.dockerTimeStampLength = dockerTimeStampLength

		//intitial chunk size
		if _,ok := container["initial_chunk_size"]; ok { //initial_chunk_size specified

			if container["initial_chunk_size"].(int) <= dockerLogHeaderSize {
				logReader.initialChunkSize = 2 * dockerLogHeaderSize
			} else{
				logReader.initialChunkSize = container["initial_chunk_size"].(int)
			}
		}else{
			logReader.initialChunkSize = dl.InitialChunkSize
		}

		//max chunk size
		if _,ok := container["max_chunk_size"]; ok { //max_chunk_size specified

			if container["max_chunk_size"].(int) <= logReader.initialChunkSize {
				logReader.maxChunkSize = 5 * logReader.initialChunkSize
			} else{
				logReader.initialChunkSize = container["max_chunk_size"].(int)
			}
		}else{
			logReader.initialChunkSize = dl.MaxChunkSize
		}

		logReader.currentChunkSize = logReader.initialChunkSize

		//Gettings target container status (It can be a case when we can attempt to read from the container that already stopped/crashed)
		contStatus,err := dl.client.ContainerInspect(dl.context,logReader.contID)
		if err != nil {
			return err
		}
		getLogsSince := ""
		getLogsSince,logReader.currentOffset = getOffset(path.Join(dl.OffsetStoragePath,logReader.contID))

		if contStatus.State.Status ==  "removing" ||
		   contStatus.State.Status == "exited" || contStatus.State.Status ==  "dead" {
			log.Printf("W! [inputs.docker_cnt_logs] container '%s' is not running!", logReader.contID)
		}

		options := types.ContainerLogsOptions{
			ShowStdout: true,
			ShowStderr: true,
			Follow:     true,
			Timestamps: logReader.dockerTimeStamps,
			Since:      getLogsSince}


		logReader.contStream, err = dl.client.ContainerLogs(dl.context, logReader.contID, options)
		if err != nil {
			return err
		}

		//Parse tags if any
		logReader.tags = map[string]string{}
		if _,ok := container["tags"]; ok { //tags specified
			for _,tag := range container["tags"].([]interface{}) {
				arr := strings.Split(tag.(string),"=")
				if len(arr) != 2 {
					return errors.Errorf("Can't parse tags from string '%s', valid format is <tag_name>=<tag_value>",tag.(string))
				}
				logReader.tags[arr[0]] = arr[1]
			}
		}
		//set container ID tag:
		logReader.tags["container_id"] = logReader.contID

		//Allocate buffer for reading logs
		logReader.buffer = make([]byte, logReader.initialChunkSize)
		logReader.msgHeaderExamined = false

		//Init channel to manage go routine
		logReader.quitFlag = make(chan bool)

		//Store
		dl.logReader[container["id"].(string)] = &logReader
	}

	//Starting log streaming
	for _,logReader := range dl.logReader {
		go dl.goGather(acc,logReader)
	}

	//Start checker
	dl.checkerQuitFlag = make(chan bool)
	go dl.checkStreamersStatus()

	//Start offset flusher
	dl.offsetQuitFlag = make(chan bool)
	go dl.flushOffset()

	return nil
}

func (dl *DockerCNTLogs) shutdownTelegraf(){
	p, err := os.FindProcess(os.Getpid())
	if err != nil {
		errorString := fmt.Errorf("E! [inputs.docker_cnt_logs] Can't get current process PID to initiate graceful shutdown: %v",err)
		log.Printf("%s",errorString)
		panic(errors.Errorf("%s",errorString))
		return
	}
	if runtime.GOOS == "windows" {
		p.Signal(os.Kill) //Interrupt is not supported on windows
	}else {
		p.Signal(os.Interrupt)
	}
}

func (dl *DockerCNTLogs) checkStreamersStatus() {

	dl.wg.Add(1)
	defer dl.wg.Done()

	for {
		select {
		case <-dl.checkerQuitFlag:
			return
		default:
			closed :=0
			for _,logReader := range dl.logReader {
				if atomic.LoadInt32(&logReader.eofReceived) > 0{
					closed++
				}
			}
			if closed == len(dl.logReader) && dl.ShutDownWhenEOF {
				log.Printf("E! [inputs.docker_cnt_logs] all target containers are stopped/killed!\n" +
					"Telegraf shutdown is requested...")
				close(dl.checkerQuitFlag)
				dl.checkerQuitFlag = nil
				dl.shutdownTelegraf()
				return
			}
		}
		time.Sleep(3 * time.Second)
	}
}

func (dl *DockerCNTLogs) flushOffset() {

	dl.wg.Add(1)
	defer dl.wg.Done()

	for {
		select {
		case <-dl.offsetQuitFlag:
			return
		default:

			for _,logReader := range dl.logReader {
				filename := path.Join(dl.OffsetStoragePath,logReader.contID)
				offset := []byte(strconv.FormatInt(atomic.LoadInt64(&logReader.currentOffset),10))
				ioutil.WriteFile(filename,offset,0777)
			}

		}
		time.Sleep(dl.offsetFlushInterval)
	}
}

func (dl *DockerCNTLogs) Stop() {

	log.Printf("D! [inputs.docker_cnt_logs] Shutting down stream checker...")

	//Stop check streamers status
	if dl.checkerQuitFlag !=nil{
		dl.checkerQuitFlag<- true
	}

	//Stop log streaming
	log.Printf("D! [inputs.docker_cnt_logs] Shutting down log streamers...")
	for _,logReader := range dl.logReader {
		if logReader.quitFlag !=nil{
			logReader.quitFlag<- true
		}
	}

	//Stop offset flushing
	time.Sleep(dl.offsetFlushInterval)
	if dl.offsetQuitFlag !=nil{
		dl.offsetQuitFlag<- true
	}

	//Wait for all go routines to complete
	dl.wg.Wait()

	log.Printf("D! [inputs.docker_cnt_logs] Closing docker streams...")
	for _,logReader := range dl.logReader {
		if logReader.contStream !=nil{
			logReader.contStream.Close()
		}
	}

	if dl.client != nil {
		dl.client.Close()
		dl.client = nil
	}

}

func init() {
	inputs.Add("docker_cnt_logs", func() telegraf.Input { return &DockerCNTLogs{} })
}
