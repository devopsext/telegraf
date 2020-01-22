package docker_cnt_logs

import (
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"strconv"
	"time"
)

type loggerWrapper struct {
	errHeader    string
	warnHeader   string
	infoHeader   string
	debugHeader  string
	headerFormat string
}

var logger *loggerWrapper = newLoggerWrapper(inputTitle)

func newLoggerWrapper(header string) *loggerWrapper {
	return &loggerWrapper{
		headerFormat: "%s %s",
		errHeader:    fmt.Sprintf("E! [%s]", header),
		warnHeader:   fmt.Sprintf("W! [%s]", header),
		infoHeader:   fmt.Sprintf("I! [%s]", header),
		debugHeader:  fmt.Sprintf("D! [%s]", header)}
}

func (lw *loggerWrapper) logE(format string, v ...interface{}) {
	log.Printf(lw.headerFormat, lw.errHeader, fmt.Sprintf(format, v...))
}

func (lw *loggerWrapper) logW(format string, v ...interface{}) {
	log.Printf(lw.headerFormat, lw.warnHeader, fmt.Sprintf(format, v...))
}

func (lw *loggerWrapper) logI(format string, v ...interface{}) {

	log.Printf(lw.headerFormat, lw.infoHeader, fmt.Sprintf(format, v...))
}
func (lw *loggerWrapper) logD(format string, v ...interface{}) {
	log.Printf(lw.headerFormat, lw.debugHeader, fmt.Sprintf(format, v...))
}

func getOffset(offsetFile string) (string, int64) {

	if _, err := os.Stat(offsetFile); !os.IsNotExist(err) {
		data, errRead := ioutil.ReadFile(offsetFile)
		if errRead != nil {
			//log.Printf("E! [inputs.docker_cnt_logs] Error reading offset file '%s', reason: %s",
			//	offsetFile, errRead.Error())
			logger.logE("Error reading offset file '%s', reason: %s",
				offsetFile, errRead.Error())
		} else {
			timeString := ""
			timeInt, err := strconv.ParseInt(string(data), 10, 64)
			if err == nil {
				timeString = time.Unix(0, timeInt).UTC().Format(time.RFC3339Nano)
			}

			//log.Printf("D! [inputs.docker_cnt_logs] Parsed offset from '%s'\nvalue: %s, %s",
			//	offsetFile, string(data), timeString)
			logger.logD("Parsed offset from '%s'\nvalue: %s, %s",
				offsetFile, string(data), timeString)
			return timeString, timeInt
		}
	}

	return "", 0
}
