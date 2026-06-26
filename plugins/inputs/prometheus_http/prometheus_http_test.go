package prometheus_http

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSample(t *testing.T) {
	c := &PrometheusHttp{}
	output := c.SampleConfig()
	require.Equal(t, output, sampleConfig, "Sample config doesn't match")
}

func TestDescription(t *testing.T) {
	c := &PrometheusHttp{}
	output := c.Description()
	require.Equal(t, output, description, "Description output is not correct")
}

func TestGetAllTagsUsesInstanceFiles(t *testing.T) {
	p1 := &PrometheusHttp{files: &sync.Map{}}
	p2 := &PrometheusHttp{files: &sync.Map{}}

	p1.files.Store("first", map[string]interface{}{"value": "one"})
	p2.files.Store("second", map[string]interface{}{"value": "two"})

	tags1 := p1.getAllTags(map[string]string{"metric": "a"}, nil, nil)
	tags2 := p2.getAllTags(map[string]string{"metric": "b"}, nil, nil)

	files1, ok := tags1["files"].(map[string]interface{})
	require.True(t, ok)
	require.Contains(t, files1, "first")
	require.NotContains(t, files1, "second")

	files2, ok := tags2["files"].(map[string]interface{})
	require.True(t, ok)
	require.Contains(t, files2, "second")
	require.NotContains(t, files2, "first")
}
