package prometheus_http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/Masterminds/sprig"
	"github.com/araddon/dateparse"

	"github.com/influxdata/telegraf"
	"github.com/influxdata/telegraf/config"
	"github.com/influxdata/telegraf/plugins/inputs"
)

// PrometheusHttpMetric struct
type PrometheusHttpMetric struct {
	Name      string `toml:"name"`
	Query     string `toml:"query"`
	Transform string `toml:"transform"`
	template  *template.Template
	Duration  config.Duration   `toml:"duration"`
	From      string            `toml:"from"`
	Step      string            `toml:"step"`
	Params    string            `toml:"params"`
	Timeout   config.Duration   `toml:"timeout"`
	Interval  config.Duration   `toml:"interval"`
	Tags      map[string]string `toml:"tags"`
	templates map[string]*template.Template
}

// PrometheusHttpPeriod struct
type PrometheusHttpPeriod struct {
	duration config.Duration
	from     string
}

// PrometheusHttp struct
type PrometheusHttp struct {
	URL           string                  `toml:"url"`
	Metrics       []*PrometheusHttpMetric `toml:"metric"`
	Duration      config.Duration         `toml:"duration"`
	From          string                  `toml:"from"`
	Timeout       config.Duration         `toml:"timeout"`
	Version       string                  `toml:"version"`
	Step          string                  `toml:"step"`
	Params        string                  `toml:"params"`
	Prefix        string                  `toml:"prefix"`
	SkipEmptyTags bool                    `toml:"skip_empty_tags"`

	Log telegraf.Logger `toml:"-"`
	acc telegraf.Accumulator
}

type PrometheusHttpPushFunc = func(when time.Time, tags map[string]string, stamp time.Time, value float64)

type PrometheusHttpDatasource interface {
	GetData(query string, period *PrometheusHttpPeriod, push PrometheusHttpPushFunc) error
}

var description = "Collect data from Prometheus http api"

// Description will return a short string to explain what the plugin does.
func (*PrometheusHttp) Description() string {
	return description
}

var sampleConfig = `
#
`

func (p *PrometheusHttpPeriod) Duration() config.Duration {
	return p.duration
}

func (p *PrometheusHttpPeriod) DurationHuman() string {
	return time.Duration(p.duration).String()
}

func (p *PrometheusHttpPeriod) From() time.Time {
	t, err := dateparse.ParseAny(p.from)
	if err == nil {
		return t
	}
	return time.Now()
}

func (p *PrometheusHttpPeriod) StartEnd() (time.Time, time.Time) {

	start := p.From()
	end := p.From().Add(time.Duration(p.Duration()))

	if start.UnixNano() > end.UnixNano() {
		t := end
		end = start
		start = t
	}

	return start, end
}

// SampleConfig will return a complete configuration example with details about each field.
func (*PrometheusHttp) SampleConfig() string {
	return sampleConfig
}

func (p *PrometheusHttp) getMetricPeriod(m *PrometheusHttpMetric) *PrometheusHttpPeriod {

	duration := p.Duration
	if m != nil && m.Duration > 0 {
		duration = m.Duration
	}

	from := p.From
	if m != nil && m.From != "" {
		from = m.From
	}

	return &PrometheusHttpPeriod{
		duration: duration,
		from:     from,
	}
}

func (p *PrometheusHttp) getTemplateValue(t *template.Template, value float64) (float64, error) {

	if t == nil {
		return value, nil
	}

	var b strings.Builder
	err := t.Execute(&b, value)
	if err != nil {
		return value, err
	}
	v := b.String()
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return value, err
	}
	return f, nil
}

func (p *PrometheusHttp) setExtraMetricTag(t *template.Template, tag string, tags map[string]string) {

	if t == nil || tag == "" {
		return
	}

	var b strings.Builder
	err := t.Execute(&b, &tags)
	if err != nil {
		p.Log.Errorf("failed to execute template: %v", err)
		return
	}
	tags[tag] = b.String()
}

func (p *PrometheusHttp) setExtraMetricTags(tags map[string]string, m *PrometheusHttpMetric) {

	if m.templates == nil {
		return
	}
	for v, t := range m.templates {
		p.setExtraMetricTag(t, v, tags)
	}
}

func (p *PrometheusHttp) setMetrics(w *sync.WaitGroup, pm *PrometheusHttpMetric) {

	timeout := pm.Timeout
	if timeout == 0 {
		timeout = p.Timeout
	}

	step := pm.Step
	if step == "" {
		step = p.Step
	}

	params := pm.Params
	if params == "" {
		params = p.Params
	}

	defer w.Done()
	var push = func(when time.Time, tgs map[string]string, stamp time.Time, value float64) {

		v, err := p.getTemplateValue(pm.template, value)
		if err != nil {
			p.Log.Error(err)
			return
		}

		fields := make(map[string]interface{})
		fields[pm.Name] = v

		millis := when.UTC().UnixMilli()
		tags := make(map[string]string)
		tags["timestamp"] = strconv.Itoa(int(millis))
		tags["duration_ms"] = strconv.Itoa(int(time.Now().UTC().UnixMilli()) - int(millis))

		for k, t := range tgs {
			if p.SkipEmptyTags && t == "" {
				continue
			}
			tags[k] = t
		}

		p.setExtraMetricTags(tags, pm)

		if math.IsNaN(value) || math.IsInf(value, 0) {
			bs, _ := json.Marshal(tags)
			p.Log.Debugf("Skipped NaN/Inf value for: %v[%v]", pm.Name, string(bs))
			return
		}
		p.acc.AddFields(p.Prefix, fields, tags, stamp)
	}

	var ds PrometheusHttpDatasource = nil
	switch p.Version {
	case "v1":
		ds = NewPrometheusHttpV1(p.Log, context.Background(), p.URL, int(timeout), step, params)
	}

	if ds != nil {
		period := p.getMetricPeriod(pm)
		err := ds.GetData(pm.Query, period, push)
		if err != nil {
			p.Log.Error(err)
		}
	}
}

func (p *PrometheusHttp) gatherMetrics() error {

	var wg sync.WaitGroup

	for _, m := range p.Metrics {

		if m.Name == "" {
			return errors.New("no metric name found")
		}

		wg.Add(1)
		go p.setMetrics(&wg, m)
	}
	wg.Wait()
	return nil
}

func (p *PrometheusHttp) getDefaultTemplate(name, value string) *template.Template {

	if value == "" {
		return nil
	}

	t, err := template.New(fmt.Sprintf("%s_template", name)).Funcs(sprig.TxtFuncMap()).Parse(value)
	if err != nil {
		p.Log.Error(err)
		return nil
	}
	return t
}

func (p *PrometheusHttp) setDefaultMetric(m *PrometheusHttpMetric) {

	if m.Name == "" {
		return
	}
	if m.Transform != "" {
		m.template = p.getDefaultTemplate(m.Name, m.Transform)
	}
	if len(m.Tags) > 0 {
		m.templates = make(map[string]*template.Template)
	}
	for k, v := range m.Tags {
		m.templates[k] = p.getDefaultTemplate(fmt.Sprintf("%s_%s", m.Name, k), v)
	}
}

// Gather is called by telegraf when the plugin is executed on its interval.
func (p *PrometheusHttp) Gather(acc telegraf.Accumulator) error {

	// Set default values
	if p.Duration == 0 {
		p.Duration = config.Duration(time.Second) * 5
	}
	if p.Timeout == 0 {
		p.Timeout = config.Duration(time.Second) * 5
	}
	if p.Version == "" {
		p.Version = "v1"
	}
	if p.Step == "" {
		p.Step = "60"
	}
	if p.Prefix == "" {
		p.Prefix = "prometheus_http"
	}
	p.acc = acc

	if len(p.Metrics) == 0 {
		return errors.New("no metrics found")
	}

	for _, m := range p.Metrics {
		p.setDefaultMetric(m)
	}

	// Gather data
	err := p.gatherMetrics()
	if err != nil {
		return err
	}

	return nil
}

func init() {
	inputs.Add("prometheus_http", func() telegraf.Input {
		return &PrometheusHttp{}
	})
}
