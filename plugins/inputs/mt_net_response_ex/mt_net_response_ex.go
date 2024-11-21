//go:debug x509negativeserial=1
//go:generate ../../../tools/readme_config_includer/generator
package net_response

import (
	"bufio"
	"crypto/md5"
	"crypto/tls"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"net/textproto"
	"regexp"
	"slices"
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
	ConnStatus  []string
	MD5         string
	MT5User     string
}

type MT5Request struct {
	server string
	port   int
	client *http.Client
}

type AuthResponse struct {
	SrvRand       string `json:"srv_rand"`
	CliRand       string `json:"cli_rand"`
	CliRandAnswer string `json:"cli_rand_answer"`
	Retcode       string `json:"retcode"`
}

func (*MtNetResponse) SampleConfig() string {
	return sampleConfig
}

func NewMT5Request(server string, port int) *MT5Request {
	return &MT5Request{
		server: server,
		port:   port,
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

func (mt5 *MT5Request) Get(path string) (string, error) {
	url := fmt.Sprintf("https://%s:%d%s", mt5.server, mt5.port, path)
	http.DefaultTransport.(*http.Transport).TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	mt5.client.Transport = http.DefaultTransport
	resp, err := mt5.client.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	return string(body), nil
}

func (mt5 *MT5Request) Post(path, body string) (string, error) {
	url := fmt.Sprintf("https://%s:%d%s", mt5.server, mt5.port, path)
	req, err := http.NewRequest("POST", url, strings.NewReader(body))
	if err != nil {
		return "", err
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Content-Length", fmt.Sprintf("%d", len(body)))

	resp, err := mt5.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	return string(responseBody), nil
}

func (mt5 *MT5Request) ParseBodyJSON(body string) (AuthResponse, error) {
	var answer AuthResponse
	if err := json.Unmarshal([]byte(body), &answer); err != nil {
		return AuthResponse{}, fmt.Errorf("failed to parse JSON: %v", err)
	}

	if answer.Retcode != "0 Done" {
		return AuthResponse{}, fmt.Errorf("retcode is not 0, got: %s", answer.Retcode)
	}

	return answer, nil
}

func ProcessAuth(md5Combined, SrvRand string) string {

	// Get bytes of MD5 hash
	md5CombinedBytes, err := hex.DecodeString(md5Combined)
	if err != nil {
		fmt.Printf("failed hex decode")
	}
	// Get bytes of SrvRand
	srvRandBytes, _ := hex.DecodeString(SrvRand)

	// Join bytes MD5 hash and SrvRand
	finalCombinedBytes := append(md5CombinedBytes, srvRandBytes...)

	// Final bytes srv_rand_answer
	md5FinalAnswer := md5.Sum(finalCombinedBytes)

	// Шаг 6: Результат в HEX-представлении
	finalHex := hex.EncodeToString(md5FinalAnswer[:])
	return finalHex
}

func (mt5 *MT5Request) Auth(md5Combined, login, build, agent string) error {
	if login == "" || build == "" || agent == "" {
		return errors.New("missing required parameters")
	}

	// Start authentication
	startPath := fmt.Sprintf("/api/auth/start?version=%s&agent=%s&login=%s&type=manager", build, agent, login)
	body, err := mt5.Get(startPath)
	if err != nil {
		return err
	}

	answer, err := mt5.ParseBodyJSON(body)
	if err != nil {
		return err
	}

	// Process auth step
	srvRandAnswer := ProcessAuth(md5Combined, answer.SrvRand)

	// Generate client random
	cliRandom := generateRandomHex()

	// Send answer
	answerPath := fmt.Sprintf("/api/auth/answer?srv_rand_answer=%s&cli_rand=%s", srvRandAnswer, cliRandom)
	body, err = mt5.Get(answerPath)
	if err != nil {
		return err
	}

	answer, err = mt5.ParseBodyJSON(body)
	if err != nil {
		return err
	}

	return nil
}

func generateRandomHex() string {
	rand.Seed(time.Now().UnixNano())
	randomBytes := make([]byte, 16)
	rand.Read(randomBytes)

	hash := md5.New()
	hash.Write(randomBytes)
	hashSum := hash.Sum(nil)

	return hex.EncodeToString(hashSum)
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
			tags["protocol"] = "tcp"
			tags["server"] = host
			tags["port"] = port
			if slices.Contains(m.ConnStatus, conn.Status) && strings.Contains(conn.Raddr.IP, host) {
				// If an active connection is found, perform a connection test

				// Start timer
				start := time.Now()

				conn, err := net.DialTimeout("tcp", ip, time.Duration(m.Timeout))

				// Handle error
				if err != nil {
					var e net.Error
					if errors.As(err, &e) && e.Timeout() {
						setResult(Timeout, fields, tags)
					} else {
						setResult(ConnectionFailed, fields, tags)
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
					setResult(ReadFailed, fields, tags)
				} else {
					// Looking for string in answer
					regEx := regexp.MustCompile(m.Expect)
					find := regEx.FindString(data)
					if find != "" {
						setResult(Success, fields, tags)
					} else {
						setResult(StringMismatch, fields, tags)
					}
				}
				fields["response_time"] = responseTime
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
			tags["protocol"] = "tcp"
			tags["server"] = host
			tags["port"] = port
			if slices.Contains(m.ConnStatus, conn.Status) && strings.Contains(conn.Raddr.IP, host) {
				// If an active connection is found, perform a connection test

				//Start timer
				start := time.Now()
				mt5 := NewMT5Request(ip, 443)
				err := mt5.Auth(m.MD5, m.MT5User, "4656", "test")
				// Stop timer
				responseTime := time.Since(start).Seconds()
				// Handle error
				if err != nil {
					var e net.Error
					if errors.As(err, &e) && e.Timeout() {
						setResult(Timeout, fields, tags)
					} else {
						setResult(ConnectionFailed, fields, tags)
					}
					return tags, fields, nil
				}
				setResult(Success, fields, tags)
				fields["response_time"] = responseTime
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
	if m.Addresses == nil {
		return errors.New("addresses cannot be empty")
	}
	if m.Type == "" {
		return errors.New("type cannot be empty")
	}
	if m.Addresses != nil {
		if m.Type == "dc" && m.Send == "" {
			return errors.New("send string cannot be empty for dc type")
		}
		if m.Type == "dc" && m.Expect == "" {
			return errors.New("expected string cannot be empty for dc type")
		}
	}
	return nil
}

// Gather is called by telegraf when the plugin is executed on its interval.
// It will call either ACGather or DCGather based on the configuration and
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
	acc.AddFields("mt_net_response", fields, tags)
	return nil
}

func setResult(result ResultType, fields map[string]interface{}, tags map[string]string) {
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
	fields["result_code"] = uint64(result)
}

func init() {
	inputs.Add("mt_net_response_ex", func() telegraf.Input {
		return &MtNetResponse{}
	})
}
