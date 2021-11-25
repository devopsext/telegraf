package grafana_dashboard

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/grafana-tools/sdk"

	"github.com/influxdata/telegraf"
	"github.com/influxdata/telegraf/config"
	"github.com/influxdata/telegraf/plugins/inputs"
)

// GrafanaDashboardMetric struct
type GrafanaDashboardMetric struct {
	Name      string
	Panels    []string
	Period    config.Duration
	Timeout   config.Duration
	Interval  config.Duration
	Tags      map[string]string
	templates map[string]*template.Template
}

// GrafanaDatasourcePushFunc func
type GrafanaDatasourcePushFunc = func(when time.Time, tags map[string]string, stamp time.Time, value float64)

// GrafanaDashboardPeriod struct
type GrafanaDashboardPeriod struct {
	AsDuration config.Duration
	AsString   string
}

// GrafanaDatasource interface
type GrafanaDatasource interface {
	GetData(t *sdk.Target, ds *sdk.Datasource, period *GrafanaDashboardPeriod, push GrafanaDatasourcePushFunc) error
}

// GrafanaDashboard struct
type GrafanaDashboard struct {
	URL        string
	APIKey     string
	Dashboards []string
	Metrics    []*GrafanaDashboardMetric `toml:"metric"`
	Period     config.Duration
	Timeout    config.Duration

	Log telegraf.Logger `toml:"-"`

	acc    telegraf.Accumulator
	client *http.Client
	ctx    context.Context
}

var description = "Collect Grafana dashboard data"

// Description will return a short string to explain what the plugin does.
func (*GrafanaDashboard) Description() string {
	return description
}

var sampleConfig = `
#
`

// SampleConfig will return a complete configuration example with details about each field.
func (*GrafanaDashboard) SampleConfig() string {
	return sampleConfig
}

func (g *GrafanaDashboard) makeHttpClient(timeout time.Duration) *http.Client {

	var transport = &http.Transport{
		Dial:                (&net.Dialer{Timeout: timeout}).Dial,
		TLSHandshakeTimeout: timeout,
		TLSClientConfig:     &tls.Config{InsecureSkipVerify: true},
	}

	var client = &http.Client{
		Timeout:   timeout,
		Transport: transport,
	}

	return client
}

func (g *GrafanaDashboard) findDashboard(c *sdk.Client, title string) (*sdk.Board, error) {
	var tags []string

	boards, err := c.SearchDashboards(g.ctx, title, false, tags...)
	if err != nil {
		return nil, err
	}

	if len(boards) > 0 {

		board, _, err := c.GetDashboardByUID(g.ctx, boards[0].UID)
		if err != nil {
			return nil, err
		}
		return &board, nil
	}
	return nil, errors.New("dashboard not found")
}

func (g *GrafanaDashboard) findDatasource(name string, dss []sdk.Datasource) *sdk.Datasource {

	for _, ds := range dss {
		if ds.Name == name {
			return &ds
		}
	}
	return nil
}

func (g *GrafanaDashboard) findDefaultDatasource(dss []sdk.Datasource) *sdk.Datasource {

	for _, ds := range dss {
		if ds.IsDefault {
			return &ds
		}
	}
	return nil
}

func (g *GrafanaDashboard) findMetrics(name string) []*GrafanaDashboardMetric {

	var r []*GrafanaDashboardMetric
	for _, m := range g.Metrics {
		for _, s := range m.Panels {
			if b, _ := regexp.MatchString(s, name); b {
				r = append(r, m)
			}
		}
	}
	return r
}

func (g *GrafanaDashboard) getMetricPeriod(m *GrafanaDashboardMetric) *GrafanaDashboardPeriod {

	period := g.Period
	if m != nil && m.Period > 0 {
		period = m.Period
	}
	periods := time.Duration(period).String()
	return &GrafanaDashboardPeriod{
		AsDuration: period,
		AsString:   periods,
	}
}

func (g *GrafanaDashboard) setExtraMetricTag(t *template.Template, tag string, tags map[string]string) {

	if t == nil || tag == "" {
		return
	}

	var b strings.Builder
	err := t.Execute(&b, &tags)
	if err != nil {
		g.Log.Errorf("failed to execute template: %v", err)
		return
	}
	tags[tag] = b.String()
}

func (g *GrafanaDashboard) setExtraMetricTags(tags map[string]string, m *GrafanaDashboardMetric) {

	if m.templates == nil {
		return
	}
	for v, t := range m.templates {
		g.setExtraMetricTag(t, v, tags)
	}
}

func (g *GrafanaDashboard) setData(b *sdk.Board, p *sdk.Panel, ds *sdk.Datasource, dss []sdk.Datasource) {

	if p.GraphPanel != nil {

		title := p.CommonPanel.Title
		var metric *GrafanaDashboardMetric
		metrics := g.findMetrics(title)
		if len(metrics) == 0 {
			return
		}
		metric = metrics[0]
		if metric.Name == "" {
			return
		}

		var wg sync.WaitGroup

		for _, t := range p.GraphPanel.Targets {

			if t.Hide {
				continue
			}

			d := ds
			if t.Datasource != "" {
				td := g.findDatasource(t.Datasource, dss)
				if td != nil {
					d = td
				}
			}

			if d == nil {
				continue
			}
			if d.Access != "proxy" {
				continue
			}

			wg.Add(1)

			if t.Interval == "" {
				t.Interval = time.Duration(metric.Interval).String()
			}

			go func(w *sync.WaitGroup, wtt, wdt string, wds *sdk.Datasource, wt *sdk.Target, wm *GrafanaDashboardMetric) {

				defer w.Done()

				var datasource GrafanaDatasource = nil

				var push = func(when time.Time, tgs map[string]string, stamp time.Time, value float64) {

					fields := make(map[string]interface{})
					fields[wm.Name] = value

					millis := when.UTC().UnixMilli()
					tags := make(map[string]string)
					tags["timestamp"] = strconv.Itoa(int(millis))
					tags["duration_ms"] = strconv.Itoa(int(time.Now().UTC().UnixMilli()) - int(millis))
					tags["title"] = wtt
					tags["datasource_type"] = wdt
					tags["datasource_name"] = wds.Name

					for k, t := range tgs {
						tags[k] = t
					}

					g.setExtraMetricTags(tags, wm)
					g.acc.AddFields("grafana_dashboard", fields, tags, stamp)
				}

				client := g.makeHttpClient(time.Duration(wm.Timeout))
				grafana := NewGrafana(g.Log, g.URL, g.APIKey, client, context.Background())

				switch wdt {
				case "prometheus":
					datasource = NewPrometheus(g.Log, grafana)
				case "influxdb":
					datasource = NewInfluxDB(g.Log, grafana)
				case "alexanderzobnin-zabbix-datasource":
					datasource = NewAlexanderzobninZabbix(g.Log, grafana)
				case "marcusolsson-json-datasource":
					datasource = NewMarcusolssonJson(g.Log, grafana)
				case "elasticsearch":
					datasource = NewElasticsearch(g.Log, grafana)
				default:
					g.Log.Debugf("%s is not implemented yet", wdt)
				}

				if datasource != nil {
					period := g.getMetricPeriod(wm)
					err := datasource.GetData(wt, wds, period, push)
					if err != nil {
						g.Log.Error(err)
					}
				}
			}(&wg, title, d.Type, d, &t, metric)
		}
		wg.Wait()
	}
}

func (g *GrafanaDashboard) processDashboard(c *sdk.Client, b *sdk.Board, dss []sdk.Datasource) {

	for _, p := range b.Panels {

		if p.RowPanel != nil {
			continue
		}
		if p.GraphPanel == nil {
			continue
		}

		var ds *sdk.Datasource

		if p.CommonPanel.Datasource != nil {
			if *p.CommonPanel.Datasource != "-- Mixed --" {
				ds = g.findDatasource(*p.CommonPanel.Datasource, dss)
				if ds == nil {
					continue
				}
			}
		} else {
			ds = g.findDefaultDatasource(dss)
		}

		g.setData(b, p, ds, dss)
	}
}

func (g *GrafanaDashboard) GrafanaGather() error {

	client := g.makeHttpClient(time.Duration(g.Timeout))
	c, err := sdk.NewClient(g.URL, g.APIKey, client)
	if err != nil {
		g.Log.Error(err)
		return err
	}
	g.client = client

	ctx := context.Background()
	dss, err := c.GetAllDatasources(ctx)
	if err != nil {
		g.Log.Error(err)
		return err
	}
	g.ctx = ctx

	for _, d := range g.Dashboards {
		b, err := g.findDashboard(c, d)
		if err != nil {
			g.Log.Errorf("%s: %s", d, err.Error())
			continue
		}
		if b == nil {
			continue
		}
		g.processDashboard(c, b, dss)
	}
	return nil
}

func (g *GrafanaDashboard) getDefaultTemplate(name, tagName, tagValue string) *template.Template {

	if tagName == "" || tagValue == "" {
		return nil
	}

	t, err := template.New(fmt.Sprintf("%s_%s_template", name, tagName)).Parse(tagValue)
	if err != nil {
		g.Log.Error(err)
		return nil
	}
	return t
}

func (g *GrafanaDashboard) setDefaultMetric(name string, m *GrafanaDashboardMetric) {

	if name == "" {
		return
	}
	if len(m.Tags) > 0 {
		m.templates = make(map[string]*template.Template)
	}
	for k, v := range m.Tags {
		m.templates[k] = g.getDefaultTemplate(name, k, v)
	}
}

// Gather is called by telegraf when the plugin is executed on its interval.
func (g *GrafanaDashboard) Gather(acc telegraf.Accumulator) error {

	// Set default values
	if g.Period == 0 {
		g.Period = config.Duration(time.Second) * 5
	}
	if g.Timeout == 0 {
		g.Timeout = config.Duration(time.Second) * 5
	}
	g.acc = acc

	if len(g.Metrics) == 0 {
		return errors.New("no metrics found")
	}

	for _, m := range g.Metrics {
		g.setDefaultMetric(m.Name, m)
	}

	// Gather data
	err := g.GrafanaGather()
	if err != nil {
		return err
	}

	return nil
}

func init() {
	inputs.Add("grafana_dashboard", func() telegraf.Input {
		return &GrafanaDashboard{}
	})
}
