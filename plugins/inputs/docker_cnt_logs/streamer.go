package docker_cnt_logs

import (
	"encoding/binary"
	"fmt"
	"io"
	"path"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/influxdata/telegraf"
	"github.com/pkg/errors"
)

type streamer struct {
	contID     string
	acc        *telegraf.Accumulator
	contStream io.ReadCloser

	msgHeaderExamined     bool
	dockerTimeStamps      bool
	interval              time.Duration
	initialChunkSize      int
	currentChunkSize      int
	maxChunkSize          int
	outputMsgStartIndex   uint
	dockerTimeStampLength uint
	buffer                []byte
	leftoverBuffer        []byte
	length                int
	endOfLineIndex        int
	tags                  map[string]string
	wg                    *sync.WaitGroup
	done                  chan bool //primary streamer go-routine
	eofReceived           bool
	currentOffset         int64
	offsetLock            *sync.Mutex
	lock                  *sync.Mutex
	logger                *loggerWrapper
}

//Service functions
func (s *streamer) isContainsHeader(str *[]byte, length int) bool {

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

	strLength := len(*str)
	if strLength > 100 {
		strLength = 100
	}

	s.logger.logD("Raw string for detecting headers (first 100 symbols):\n%s...\n", (*str)[:strLength-1])
	s.logger.logD("First 4 bytes: '%v,%v,%v,%v', string representation: '%s'",
		(*str)[0], (*str)[1], (*str)[2], (*str)[3], (*str)[0:4])
	s.logger.logD("Big endian value: %d", binary.BigEndian.Uint32((*str)[4:dockerLogHeaderSize]))

	//Examine first 4 bytes to detect if they match to header structure (see above)
	if ((*str)[0] == 0x0 || (*str)[0] == 0x1 || (*str)[0] == 0x2) &&
		((*str)[1] == 0x0 && (*str)[2] == 0x0 && (*str)[3] == 0x0) &&
		binary.BigEndian.Uint32((*str)[4:dockerLogHeaderSize]) >= 2 /*Encoding big endian*/ {
		//binary.BigEndian.Uint32((*str)[4:dockerLogHeaderSize]) - calculates message length.
		//Minimum message length with timestamp is 32 (timestamp (30 symbols) + space + '\n' = 32.
		//But in case you switch timestamp off it will be 2 (space + '\n')

		s.logger.logI("Detected: log messages from docker API streamed WITH headers...")
		result = true

	} else {
		s.logger.logI("Detected: log messages from docker API streamed WITHOUT headers...")
		result = false
	}

	return result
}

//If there is no new line in interval [eolIndex-HeaderSize,eolIndex+HeaderSize],
//then we are definitely not in the middle of header, otherwise, we are.
func (s *streamer) isNewLineInMsgHeader(str *[]byte, eolIndex int) bool {
	//Edge case:
	if eolIndex == dockerLogHeaderSize {
		return false
	}

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

func (s *streamer) getInputIntervalDuration() (dur time.Duration) {
	// As agent.accumulator type is not exported, we need to use reflect, for getting the value of
	// 'interval' for this input
	// The code below should be revised, in case agent.accumulator type would be changed.
	// Anyway, all possible checks on the data structure are made to handle types change.
	emptyValue := reflect.Value{}
	agentAccumulator := reflect.ValueOf(s.acc).Elem()
	if agentAccumulator.Kind() == reflect.Struct {
		if agentAccumulator.FieldByName("maker") == emptyValue { //Empty value
			s.logger.logW("Error while parsing agent.accumulator type, filed 'maker'"+
				" is not found.\nDefault pooling duration '%d' nano sec. will be used", defaultPolingIntervalNS)
			dur = defaultPolingIntervalNS
		} else {
			runningInput := reflect.Indirect(agentAccumulator.FieldByName("maker").Elem())
			if reflect.Indirect(runningInput).FieldByName("Config") == emptyValue {
				s.logger.logW("Error while parsing models.RunningInput type, filed "+
					"'Config' is not found.\nDefault pooling duration '%d' nano sec. will be used", defaultPolingIntervalNS)
				dur = defaultPolingIntervalNS
			} else {
				if reflect.Indirect(reflect.Indirect(runningInput).FieldByName("Config").Elem()).FieldByName("Interval") == emptyValue {
					s.logger.logW("Error while parsing models.InputConfig type, filed 'Interval'"+
						" is not found.\nDefault pooling duration '%d' nano sec. will be used", defaultPolingIntervalNS)
					dur = defaultPolingIntervalNS

				} else {
					interval := reflect.Indirect(reflect.Indirect(runningInput).FieldByName("Config").Elem()).FieldByName("Interval")
					if interval.Kind() == reflect.Int64 {
						dur = time.Duration(interval.Int())
					} else {
						s.logger.logW("Error while parsing models.RunningInput.Interval type, filed "+
							" is not of type 'int'.\nDefault pooling duration '%d' nano sec. will be used", defaultPolingIntervalNS)
						dur = defaultPolingIntervalNS
					}
				}

			}

		}

	}

	return
}

func newStreamer(acc *telegraf.Accumulator, dl *DockerCNTLogs, container map[string]interface{}) (*streamer, error) {
	var err error
	var getLogsSince string
	var contStatus types.ContainerJSON
	s := &streamer{}
	s.wg = &dl.wg
	s.acc = acc
	s.logger = newLoggerWrapper(inputTitle)
	s.contID = container["id"].(string)

	if _, ok := container["interval"]; ok { //inetrval is specified
		s.interval, err = time.ParseDuration(container["interval"].(string))
		if err != nil {
			return nil, errors.Errorf("Can't parse interval from string '%s', reason: %s", container["interval"].(string), err.Error())
		}
	} else {
		s.interval = s.getInputIntervalDuration()
	}

	s.dockerTimeStamps = !dl.disableTimeStampsStreaming //Default behaviour - stream logs with time-stamps
	s.dockerTimeStampLength = dockerTimeStampLength

	//intitial chunk size
	if _, ok := container["initial_chunk_size"]; ok { //initial_chunk_size specified

		if int(container["initial_chunk_size"].(int64)) <= dockerLogHeaderSize {
			s.initialChunkSize = 2 * dockerLogHeaderSize
		} else {
			s.initialChunkSize = int(container["initial_chunk_size"].(int64))
		}
	} else {
		s.initialChunkSize = dl.InitialChunkSize
	}

	//max chunk size
	if _, ok := container["max_chunk_size"]; ok { //max_chunk_size specified

		if int(container["max_chunk_size"].(int64)) <= s.initialChunkSize {
			s.maxChunkSize = 5 * s.initialChunkSize
		} else {
			s.maxChunkSize = int(container["max_chunk_size"].(int64))
		}
	} else {
		s.maxChunkSize = dl.MaxChunkSize
	}

	s.currentChunkSize = s.initialChunkSize

	//Gettings target container status (It can be a case when we can attempt
	//to read from the container that already stopped/crashed)
	contStatus, err = dl.client.ContainerInspect(dl.context, s.contID)
	if err != nil {
		return nil, err
	}

	s.offsetLock = &sync.Mutex{} //Used to lock 'currentOffset' when reading/writing
	getLogsSince, s.currentOffset = getOffset(path.Join(dl.OffsetStoragePath, s.contID))

	if contStatus.State.Status == "removing" ||
		contStatus.State.Status == "exited" || contStatus.State.Status == "dead" {
		s.logger.logW("container '%s' is not running!", s.contID)
	}

	options := types.ContainerLogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     true,
		Timestamps: s.dockerTimeStamps,
		Since:      getLogsSince}

	s.contStream, err = dl.client.ContainerLogs(dl.context, s.contID, options)
	if err != nil {
		return nil, err
	}

	//Parse tags if any
	s.tags = map[string]string{}
	if _, ok := container["tags"]; ok { //tags specified
		for _, tag := range container["tags"].([]interface{}) {
			arr := strings.Split(tag.(string), "=")
			if len(arr) != 2 {
				return nil, errors.Errorf("Can't parse tags from string '%s', valid format is <tag_name>=<tag_value>", tag.(string))
			}
			s.tags[arr[0]] = arr[1]
		}
	}
	//set container ID tag:
	s.tags["container_id"] = s.contID

	//Allocate buffer for reading logs
	s.buffer = make([]byte, s.initialChunkSize)
	s.msgHeaderExamined = false

	//Init channel to manage go routine
	s.done = make(chan bool)
	s.lock = &sync.Mutex{}

	return s, nil
}

func (s *streamer) stream(done <-chan bool) {

	var err error

	s.wg.Add(1)
	defer s.wg.Done()

	eofReceived := false
	for {
		select {
		case <-done:
			return
		default:
			//Iterative reads by chunks
			// While reading in chunks, there are 2 general cases:
			// 1. Either full buffer (it means that the message either fit to chunkSize or exceed it.
			// To figure out if it exceed we need to check if the buffer ends with "\r\n"

			// 2. Or partially filled buffer. In this case the rest of the buffer is '\0'

			// Read from docker API
			s.length, err = s.contStream.Read(s.buffer) //Can be a case when API returns lr.length==0, and err==nil

			if err != nil {
				if err.Error() == "EOF" { //Special case, need to flush data and exit
					eofReceived = true
				} else {
					select {
					case <-done: //In case the goroutine was signaled, the stream would be closed, to unblock
						//the read operation. Moreover read operations can return error in this case (if stream is closed).
						//That's why we don't need to print error, as it is expected behaviour...
						return
					default:
						(*s.acc).AddError(fmt.Errorf("Read error from container '%s': %v", s.contID, err))
						return
					}

				}
			}

			if needMoreData := s.processReadData(); needMoreData {
				continue
			}

			//Since read from API can return lr.length==0, and err==nil, we need to additionally check the boundaries
			if len(s.buffer) > 0 && s.endOfLineIndex > 0 {
				s.sendData()
			}

			//Control the size of buffer
			if len(s.buffer) > s.maxChunkSize {
				s.buffer = nil
				s.buffer = make([]byte, s.currentChunkSize)
			}

			if eofReceived {
				s.logger.logE("Container '%s': 'EOF' received.", s.contID)

				s.lock.Lock()
				s.eofReceived = eofReceived
				s.lock.Unlock()

				return
			}

		}

		time.Sleep(s.interval)
	}
}

func (s *streamer) processReadData() (readMore bool) {
	// There are 2 general cases when we read logs data from docker API into buffer of specifed size:
	// 1. Either full buffer (it means that the message either fit to chunkSize or exceed it.
	// To figure out if it exceed we need to check if the buffer ends with "\r\n"

	// 2. Or partially filled buffer. In this case the rest of the buffer is '\0'

	if !s.msgHeaderExamined {
		if s.isContainsHeader(&s.buffer, s.length) {
			s.outputMsgStartIndex = dockerLogHeaderSize //Header is in the string, need to strip it out...
		} else {
			s.outputMsgStartIndex = 0 //No header in the output, start from the 1st letter.
		}
		s.msgHeaderExamined = true
	}

	if len(s.leftoverBuffer) > 0 { //append leftover from previous iteration
		s.buffer = append(s.leftoverBuffer, s.buffer...)
		s.length += len(s.leftoverBuffer)

		//Erasing leftover buffer once used:
		s.leftoverBuffer = nil
	}

	if s.length != 0 {
		//This covers the 2nd case.
		s.endOfLineIndex = s.length - 1
	} else {
		//The buffer is filled with '\0' until the end even if there is no data at all,
		//In this case, lr.length == 0 as it shows the amount of actually read data, but len(lr.buffer)
		// will be equal to cap(lr.buffer), as the buffer will be filled out with '\0'
		s.endOfLineIndex = 0
	}

	//Check if 1st case is in place. If it is, then we need to adjust s.endOfLineIndex, and move some data to leftover buffer
	if s.length == len(s.buffer) && s.length > 0 {
		//Seek last line end (from the end), ignoring the case when this line end is in the message header
		for ; s.endOfLineIndex >= int(s.outputMsgStartIndex); s.endOfLineIndex-- {
			if s.buffer[s.endOfLineIndex] == '\n' {

				//Skip '\n' if there are headers and '\n' is inside header
				if s.outputMsgStartIndex > 0 && s.isNewLineInMsgHeader(&s.buffer, s.endOfLineIndex) {
					continue
				}

				if s.endOfLineIndex != s.length-1 {
					// Moving everything that is after s.endOfLineIndex to leftover buffer
					s.leftoverBuffer = nil
					s.leftoverBuffer = make([]byte, (s.length-1)-s.endOfLineIndex)
					copy(s.leftoverBuffer, s.buffer[s.endOfLineIndex+1:])
				}
				break
			}
		}

		//Check if line end is not found, then we either got very long message or buffer is really small
		if s.endOfLineIndex == int(s.outputMsgStartIndex-1) { //This means that the end of line is not found
			//Buffer holds one string that is not terminated (this is a part of the message)
			//We need simply to move it into leftover buffer
			//and grow current chunk size if limit is not exceeded
			s.leftoverBuffer = nil
			s.leftoverBuffer = make([]byte, len(s.buffer))
			copy(s.leftoverBuffer, s.buffer)

			//Grow chunk size
			if s.currentChunkSize*2 < s.maxChunkSize {
				s.currentChunkSize = s.currentChunkSize * 2
				s.buffer = nil
				s.buffer = make([]byte, s.currentChunkSize)
			}

			return true
		}

	}
	return false
}

func (s *streamer) sendData() {
	//Parsing the buffer line by line and send data to accumulator
	totalLineLength := 0
	var timeStamp time.Time
	var field map[string]interface{}
	var tags = s.tags
	var err error

	for i := 0; i <= s.endOfLineIndex; i = i + totalLineLength {
		field = make(map[string]interface{})
		//Checking boundaries:
		if i+int(s.outputMsgStartIndex) > s.endOfLineIndex { //sort of garbage
			timeStamp = time.Now()
			field["value"] = fmt.Sprintf("%s", s.buffer[i:s.endOfLineIndex])
			(*s.acc).AddFields("stream", field, tags, timeStamp)
			break
		}

		//Looking for the end of the line (skipping index)
		totalLineLength = 0
		for j := i + int(s.outputMsgStartIndex); j <= s.endOfLineIndex; j++ {
			if s.buffer[j] == '\n' {
				totalLineLength = j - i + 1 //Include '\n'
				break
			}
		}
		if totalLineLength == 0 {
			totalLineLength = (s.endOfLineIndex + 1) - i
		}

		//Getting stream type (if header persist)
		if s.outputMsgStartIndex > 0 {
			if s.buffer[i] == 0x1 {
				tags["stream"] = "stdout"
			} else if s.buffer[i] == 0x2 {
				tags["stream"] = "stderr"
			} else if s.buffer[i] == 0x0 {
				tags["stream"] = "stdin"
			}
		} else {
			tags["stream"] = "interactive"
		}

		if uint(totalLineLength) < s.outputMsgStartIndex+s.dockerTimeStampLength+1 || !s.dockerTimeStamps { //no time stamp

			timeStamp = time.Now()
			field["value"] = fmt.Sprintf("%s", s.buffer[i+int(s.outputMsgStartIndex):i+totalLineLength])

		} else {

			timeStamp, err = time.Parse(time.RFC3339Nano,
				fmt.Sprintf("%s",
					s.buffer[i+int(s.outputMsgStartIndex):i+int(s.outputMsgStartIndex)+int(s.dockerTimeStampLength)]))

			if err != nil {

				(*s.acc).AddError(fmt.Errorf("Can't parse time stamp from string, container '%s': "+
					"%v. Raw message string:\n%s\nOutput msg start index: %d",
					s.contID, err, s.buffer[i:i+totalLineLength], s.outputMsgStartIndex))

				s.logger.logE("\n=========== buffer[:lr.endOfLineIndex] ===========\n"+
					"%s\n=========== ====== ===========\n", s.buffer[:s.endOfLineIndex])
			}
			field["value"] = fmt.Sprintf("%s",
				s.buffer[i+int(s.outputMsgStartIndex)+int(s.dockerTimeStampLength)+1:i+totalLineLength])
		}

		(*s.acc).AddFields("stream", field, tags, timeStamp)

		for k := range field {
			delete(field, k)
		}

		//Saving offset
		s.offsetLock.Lock()
		s.currentOffset += timeStamp.UTC().UnixNano() - s.currentOffset + 1 //+1 (nanosecond) here prevents to include current message
		s.offsetLock.Unlock()
	}
}
