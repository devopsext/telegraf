//go:generate ../../../tools/readme_config_includer/generator
package net_response

import (
	"bufio"
	_ "embed"
	"errors"
	"net"
	"net/textproto"
	"regexp"
	"strings"
	"time"

	"github.com/influxdata/telegraf"
	"github.com/influxdata/telegraf/config"
	"github.com/influxdata/telegraf/plugins/inputs"

	utils "github.com/shirou/gopsutil/net"
)

//go:embed sample.conf
var sampleConfig string

type ResultType uint64

const (
	Success          ResultType = 0
	Timeout          ResultType = 1
	ConnectionFailed ResultType = 2
	ReadFailed       ResultType = 3
	StringMismatch   ResultType = 4
)

// NetResponse struct
type MtNetResponse struct {
	Addresses   []string
	Timeout     config.Duration
	ReadTimeout config.Duration
	Send        string
	Expect      string
	Type        string
}

func (*MtNetResponse) SampleConfig() string {
	return sampleConfig
}

// DCGather will execute if there are DC type defined in the configuration.
func (m *MtNetResponse) DCGather() (map[string]string, map[string]interface{}, error) {
	// Prepare returns
	tags := make(map[string]string)
	fields := make(map[string]interface{})
	// Get TCP connections
	connections, err := utils.Connections("tcp")
	if err != nil {
		return nil, nil, err
	}
	// Check if there are active connections with the IP from the DC list
	for _, ip := range m.Addresses {
		for _, conn := range connections {
			// Prepare host and port
			host, port, err := net.SplitHostPort(ip)
			if err != nil {
				return nil, nil, err
			}
			if conn.Status == "ESTABLISHED" && strings.Contains(conn.Raddr.IP, host) {
				// If an active connection is found, perform a connection test

				// Start timer
				start := time.Now()

				conn, err := net.DialTimeout("tcp", ip, time.Duration(m.Timeout))

				// Handle error
				if err != nil {
					var e net.Error
					if errors.As(err, &e) && e.Timeout() {
						setResult(Timeout, tags)
					} else {
						setResult(ConnectionFailed, tags)
					}
					return tags, fields, nil
				}

				defer conn.Close()

				// Send data
				_, err = conn.Write([]byte(m.Send))
				if err != nil {
					return nil, nil, err
				}
				reader := bufio.NewReader(conn)
				tp := textproto.NewReader(reader)
				data, err := tp.ReadLine()

				// Stop timer
				responseTime := time.Since(start).Seconds()

				// Handle error
				if err != nil {
					setResult(ReadFailed, tags)
				} else {
					// Looking for string in answer
					regEx := regexp.MustCompile(m.Expect)
					find := regEx.FindString(data)
					if find != "" {
						setResult(Success, tags)
					} else {
						setResult(StringMismatch, tags)
					}
				}
				fields["response_time"] = responseTime
				tags["protocol"] = "tcp"
				tags["server"] = host
				tags["port"] = port
				return tags, fields, nil
			}
		}
	}
	return nil, nil, err
}

func (m *MtNetResponse) ACGather() (map[string]string, map[string]interface{}, error) {
	// Prepare returns
	tags := make(map[string]string)
	fields := make(map[string]interface{})
	// Get TCP connections
	connections, err := utils.Connections("tcp")
	if err != nil {
		return nil, nil, err
	}
	// Check if there are active connections with the IP from the DC list
	for _, ip := range m.Addresses {
		for _, conn := range connections {
			// Prepare host and port
			host, port, err := net.SplitHostPort(ip)
			if err != nil {
				return nil, nil, err
			}
			if conn.Status == "ESTABLISHED" && strings.Contains(conn.Raddr.IP, host) {
				// If an active connection is found, perform a connection test

				//Start timer
				start := time.Now()
				conn, err := net.DialTimeout("tcp", ip, time.Duration(m.Timeout))
				// Stop timer
				responseTime := time.Since(start).Seconds()
				// Handle error
				if err != nil {
					var e net.Error
					if errors.As(err, &e) && e.Timeout() {
						setResult(Timeout, tags)
					} else {
						setResult(ConnectionFailed, tags)
					}
					return tags, fields, nil
				}
				defer conn.Close()
				setResult(Success, tags)
				fields["response_time"] = responseTime
				tags["protocol"] = "tcp"
				tags["server"] = host
				tags["port"] = port
				return tags, fields, nil
			}
		}
	}
	return nil, nil, err
}

// Init performs one time setup of the plugin and returns an error if the
// configuration is invalid.
func (m *MtNetResponse) Init() error {
	// Set default values
	if m.Timeout == 0 {
		m.Timeout = config.Duration(time.Second)
	}
	if m.ReadTimeout == 0 {
		m.ReadTimeout = config.Duration(time.Second)
	}

	if m.Dc != nil {
		if m.Send == "" {
			return errors.New("send string cannot be empty")
		}
		if m.Expect == "" {
			return errors.New("expected string cannot be empty")
		}
	}
	if m.Dc == nil && m.Ac == nil {
		return errors.New("dc and ac cannot be empty")
	}
	if m.Type == "" {
		return errors.New("type cannot be empty")
	}
	return nil
}

// Gather is called by telegraf when the plugin is executed on its interval.
// It will call either UDPGather or TCPGather based on the configuration and
// also fill an Accumulator that is supplied.
func (m *MtNetResponse) Gather(acc telegraf.Accumulator) error {
	// Prepare data
	tags := map[string]string{}
	var fields map[string]interface{}
	var returnTags map[string]string
	var err error
	// Gather data
	switch m.Type {
	case "ac":
		returnTags, fields, err = m.ACGather()
		if err != nil {
			return err
		}
		tags["type"] = "ac"
	case "dc":
		returnTags, fields, err = m.DCGather()
		if err != nil {
			return err
		}
		tags["type"] = "dc"
	}
	// Merge the tags
	for k, v := range returnTags {
		tags[k] = v
	}
	// Add metrics
	acc.AddFields("net_response", fields, tags)
	return nil
}

func setResult(result ResultType, tags map[string]string) {
	var tag string
	switch result {
	case Success:
		tag = "success"
	case Timeout:
		tag = "timeout"
	case ConnectionFailed:
		tag = "connection_failed"
	case ReadFailed:
		tag = "read_failed"
	case StringMismatch:
		tag = "string_mismatch"
	}

	tags["result"] = tag
}

func init() {
	inputs.Add("mt_net_response_ex", func() telegraf.Input {
		return &MtNetResponse{}
	})
}
