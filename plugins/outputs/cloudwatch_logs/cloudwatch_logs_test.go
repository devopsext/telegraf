package cloudwatch_logs

import (
	"fmt"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go/service/cloudwatchlogs"
	"github.com/influxdata/telegraf"
	"github.com/influxdata/telegraf/testutil"
	"github.com/stretchr/testify/require"
)

type mockCloudWatchLogs struct {
	logStreamName   string
	pushedLogEvents []cloudwatchlogs.InputLogEvent
}

func (c *mockCloudWatchLogs) Init(lsName string) {
	c.logStreamName = lsName
	c.pushedLogEvents = make([]cloudwatchlogs.InputLogEvent, 0)
}

func (c *mockCloudWatchLogs) DescribeLogGroups(input *cloudwatchlogs.DescribeLogGroupsInput) (*cloudwatchlogs.DescribeLogGroupsOutput, error) {
	return nil, nil
}

func (c *mockCloudWatchLogs) DescribeLogStreams(input *cloudwatchlogs.DescribeLogStreamsInput) (*cloudwatchlogs.DescribeLogStreamsOutput, error) {
	arn := "arn"
	creationTime := time.Now().Unix()
	sequenceToken := "arbitraryToken"
	output := &cloudwatchlogs.DescribeLogStreamsOutput{
		LogStreams: []*cloudwatchlogs.LogStream{
			{
				Arn:                 &arn,
				CreationTime:        &creationTime,
				FirstEventTimestamp: &creationTime,
				LastEventTimestamp:  &creationTime,
				LastIngestionTime:   &creationTime,
				LogStreamName:       &c.logStreamName,
				UploadSequenceToken: &sequenceToken,
			}},
		NextToken: &sequenceToken,
	}
	return output, nil
}
func (c *mockCloudWatchLogs) CreateLogStream(input *cloudwatchlogs.CreateLogStreamInput) (*cloudwatchlogs.CreateLogStreamOutput, error) {
	return nil, nil
}
func (c *mockCloudWatchLogs) PutLogEvents(input *cloudwatchlogs.PutLogEventsInput) (*cloudwatchlogs.PutLogEventsOutput, error) {
	sequenceToken := "arbitraryToken"
	output := &cloudwatchlogs.PutLogEventsOutput{NextSequenceToken: &sequenceToken}
	//Saving messages
	for _, event := range input.LogEvents {
		c.pushedLogEvents = append(c.pushedLogEvents, *event)
	}

	return output, nil
}

func RandStringBytes(n int) string {
	const letterBytes = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
	b := make([]byte, n)
	for i := range b {
		b[i] = letterBytes[rand.Intn(len(letterBytes))]
	}
	return string(b)
}

func TestConnect(t *testing.T) {
	//mock cloudwatch logs endpoint that is used only in plugin.Connect
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w,
			`{
				   "logGroups": [ 
					  { 
						 "arn": "string",
						 "creationTime": 123456789,
						 "kmsKeyId": "string",
						 "logGroupName": "TestLogGroup",
						 "metricFilterCount": 1,
						 "retentionInDays": 10,
						 "storedBytes": 0
					  }
				   ]
				}`)
	}))
	defer ts.Close()

	plugin := new(CloudWatchLogs)
	plugin.Region = "eu-central-1"
	plugin.AccessKey = "dummy"
	plugin.SecretKey = "dummy"
	plugin.EndpointURL = ts.URL
	plugin.LogGroup = "TestLogGroup"
	plugin.LogStream = "tag:source"
	plugin.LDMetricName = "docker_log"
	plugin.LDSource = "field:message"
	require.Nil(t, plugin.Connect())
}

func TestWrite(t *testing.T) {

	//mock cloudwatch logs endpoint that is used only in plugin.Connect
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w,
			`{
				   "logGroups": [ 
					  { 
						 "arn": "string",
						 "creationTime": 123456789,
						 "kmsKeyId": "string",
						 "logGroupName": "TestLogGroup",
						 "metricFilterCount": 1,
						 "retentionInDays": 1,
						 "storedBytes": 0
					  }
				   ]
				}`)
	}))
	defer ts.Close()

	plugin := new(CloudWatchLogs)
	plugin.Region = "eu-central-1"
	plugin.AccessKey = "dummy"
	plugin.SecretKey = "dummy"
	plugin.EndpointURL = ts.URL
	plugin.LogGroup = "TestLogGroup"
	plugin.LogStream = "tag:source"
	plugin.LDMetricName = "docker_log"
	plugin.LDSource = "field:message"
	require.Nil(t, plugin.Connect())

	tests := []struct {
		name                 string
		logStreamName        string
		metrics              []telegraf.Metric
		expectedMetricsOrder map[int]int //map[<index of pushed log event>]<index of corresponding metric>
		expectedErrorMessage string
		expectedMetricsCount int
	}{
		{
			name:                 "Sorted by timestamp log entries",
			logStreamName:        "deadbeef",
			expectedMetricsOrder: map[int]int{0: 0, 1: 1},
			expectedMetricsCount: 2,
			metrics: []telegraf.Metric{
				testutil.MustMetric(
					"docker_log",
					map[string]string{
						"container_name":    "telegraf",
						"container_image":   "influxdata/telegraf",
						"container_version": "1.11.0",
						"stream":            "tty",
						"source":            "deadbeef",
					},
					map[string]interface{}{
						"container_id": "deadbeef",
						"message":      "Sorted: message #1",
					},
					time.Now().Add(-time.Minute),
				),
				testutil.MustMetric(
					"docker_log",
					map[string]string{
						"container_name":    "telegraf",
						"container_image":   "influxdata/telegraf",
						"container_version": "1.11.0",
						"stream":            "tty",
						"source":            "deadbeef",
					},
					map[string]interface{}{
						"container_id": "deadbeef",
						"message":      "Sorted: message #2",
					},
					time.Now(),
				),
			},
		},
		{
			name:                 "Unsorted log entries",
			logStreamName:        "deadbeef",
			expectedMetricsOrder: map[int]int{0: 1, 1: 0},
			expectedMetricsCount: 2,
			metrics: []telegraf.Metric{
				testutil.MustMetric(
					"docker_log",
					map[string]string{
						"container_name":    "telegraf",
						"container_image":   "influxdata/telegraf",
						"container_version": "1.11.0",
						"stream":            "tty",
						"source":            "deadbeef",
					},
					map[string]interface{}{
						"container_id": "deadbeef",
						"message":      "Unsorted: message #1",
					},
					time.Now(),
				),
				testutil.MustMetric(
					"docker_log",
					map[string]string{
						"container_name":    "telegraf",
						"container_image":   "influxdata/telegraf",
						"container_version": "1.11.0",
						"stream":            "tty",
						"source":            "deadbeef",
					},
					map[string]interface{}{
						"container_id": "deadbeef",
						"message":      "Unsorted: message #2",
					},
					time.Now().Add(-time.Minute),
				),
			},
		},
		{
			name:                 "Too old log entry & log entry in the future",
			logStreamName:        "deadbeef",
			expectedMetricsCount: 0,
			metrics: []telegraf.Metric{
				testutil.MustMetric(
					"docker_log",
					map[string]string{
						"container_name":    "telegraf",
						"container_image":   "influxdata/telegraf",
						"container_version": "1.11.0",
						"stream":            "tty",
						"source":            "deadbeef",
					},
					map[string]interface{}{
						"container_id": "deadbeef",
						"message":      "message #1",
					},
					time.Now().Add(-maxPastLogEventTimeOffset).Add(-time.Hour),
				),
				testutil.MustMetric(
					"docker_log",
					map[string]string{
						"container_name":    "telegraf",
						"container_image":   "influxdata/telegraf",
						"container_version": "1.11.0",
						"stream":            "tty",
						"source":            "deadbeef",
					},
					map[string]interface{}{
						"container_id": "deadbeef",
						"message":      "message #2",
					},
					time.Now().Add(maxFutureLogEventTimeOffset).Add(time.Hour),
				),
			},
		},
		{
			name:                 "Oversized log entry",
			logStreamName:        "deadbeef",
			expectedMetricsCount: 0,
			metrics: []telegraf.Metric{
				testutil.MustMetric(
					"docker_log",
					map[string]string{
						"container_name":    "telegraf",
						"container_image":   "influxdata/telegraf",
						"container_version": "1.11.0",
						"stream":            "tty",
						"source":            "deadbeef",
					},
					map[string]interface{}{
						"container_id": "deadbeef",
						//Here comes very long message
						"message": RandStringBytes(maxLogMessageLength + 1),
					},
					time.Now().Add(-time.Minute),
				),
			},
		},
		{
			name:                 "Batching log entries",
			logStreamName:        "deadbeef",
			expectedMetricsOrder: map[int]int{0: 0, 1: 1, 2: 2, 3: 3, 4: 4},
			expectedMetricsCount: 5,
			metrics: []telegraf.Metric{
				testutil.MustMetric(
					"docker_log",
					map[string]string{
						"container_name":    "telegraf",
						"container_image":   "influxdata/telegraf",
						"container_version": "1.11.0",
						"stream":            "tty",
						"source":            "deadbeef",
					},
					map[string]interface{}{
						"container_id": "deadbeef",
						//Here comes very long message to cause message batching
						"message": "batch1 message1:" + RandStringBytes(maxLogMessageLength-16),
					},
					time.Now().Add(-4*time.Minute),
				),
				testutil.MustMetric(
					"docker_log",
					map[string]string{
						"container_name":    "telegraf",
						"container_image":   "influxdata/telegraf",
						"container_version": "1.11.0",
						"stream":            "tty",
						"source":            "deadbeef",
					},
					map[string]interface{}{
						"container_id": "deadbeef",
						//Here comes very long message to cause message batching
						"message": "batch1 message2:" + RandStringBytes(maxLogMessageLength-16),
					},
					time.Now().Add(-3*time.Minute),
				),
				testutil.MustMetric(
					"docker_log",
					map[string]string{
						"container_name":    "telegraf",
						"container_image":   "influxdata/telegraf",
						"container_version": "1.11.0",
						"stream":            "tty",
						"source":            "deadbeef",
					},
					map[string]interface{}{
						"container_id": "deadbeef",
						//Here comes very long message to cause message batching
						"message": "batch1 message3:" + RandStringBytes(maxLogMessageLength-16),
					},
					time.Now().Add(-2*time.Minute),
				),
				testutil.MustMetric(
					"docker_log",
					map[string]string{
						"container_name":    "telegraf",
						"container_image":   "influxdata/telegraf",
						"container_version": "1.11.0",
						"stream":            "tty",
						"source":            "deadbeef",
					},
					map[string]interface{}{
						"container_id": "deadbeef",
						//Here comes very long message to cause message batching
						"message": "batch1 message4:" + RandStringBytes(maxLogMessageLength-16),
					},
					time.Now().Add(-time.Minute),
				),
				testutil.MustMetric(
					"docker_log",
					map[string]string{
						"container_name":    "telegraf",
						"container_image":   "influxdata/telegraf",
						"container_version": "1.11.0",
						"stream":            "tty",
						"source":            "deadbeef",
					},
					map[string]interface{}{
						"container_id": "deadbeef",
						"message":      "batch2 message1",
					},
					time.Now(),
				),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			//Overwrite cloud watch log endpoint
			mockCwl := &mockCloudWatchLogs{}
			mockCwl.Init(tt.logStreamName)
			plugin.svc = mockCwl
			require.Nil(t, plugin.Write(tt.metrics))
			require.Equal(t, tt.expectedMetricsCount, len(mockCwl.pushedLogEvents))

			for index, elem := range mockCwl.pushedLogEvents {
				require.Equal(t, *elem.Message, tt.metrics[tt.expectedMetricsOrder[index]].Fields()["message"])
				require.Equal(t, *elem.Timestamp, tt.metrics[tt.expectedMetricsOrder[index]].Time().UnixNano()/1000000)
			}
		})
	}
}
