package prometheus_http

import (
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

func TestGetAllTagsUsesGlobalFiles(t *testing.T) {
	resetGlobalFileCache()
	t.Cleanup(resetGlobalFileCache)

	globalFiles.Store("shared", map[string]interface{}{"value": "one"})

	tags := (&PrometheusHttp{}).getAllTags(map[string]string{"metric": "a"}, nil, nil)

	files, ok := tags["files"].(map[string]interface{})
	require.True(t, ok)
	require.Contains(t, files, "shared")
}

func TestOnConfigReloadClearsGlobalFiles(t *testing.T) {
	resetGlobalFileCache()
	t.Cleanup(resetGlobalFileCache)

	globalFiles.Store("shared", map[string]interface{}{"value": "one"})
	globalHashes.Store("shared", uint64(123))

	(&PrometheusHttp{}).OnConfigReload()

	files := (&PrometheusHttp{}).getAllTags(nil, nil, nil)["files"].(map[string]interface{})
	require.Empty(t, files)

	_, ok := globalHashes.Load("shared")
	require.False(t, ok)
}
