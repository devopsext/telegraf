package grafana_dashboard

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"net"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"text/template"
	"time"

	jsonata "github.com/blues/jsonata-go"
	"github.com/grafana-tools/sdk"

	"github.com/influxdata/telegraf"
	"github.com/influxdata/telegraf/config"
	"github.com/influxdata/telegraf/plugins/inputs"
)

// GrafanaDashboardMetric
type GrafanaDashboardMetric struct {
	Panels    []string
	Period    config.Duration
	Tags      map[string]string
	templates map[string]*template.Template
}

// GrafanaDashboardAvailability struct
type GrafanaDashboardAvailability struct {
	GrafanaDashboardMetric
}

// GrafanaDashboardLatency struct
type GrafanaDashboardLatency struct {
	GrafanaDashboardMetric
}

// GrafanaDashboardMetrics struct
type GrafanaDashboardMetrics struct {
	Availability *GrafanaDashboardAvailability
	Latency      *GrafanaDashboardLatency
}

// GrafanaDashboard struct
type GrafanaDashboard struct {
	URL          string
	APIKey       string
	Dashboards   []string
	Availability []*GrafanaDashboardAvailability
	Latency      []*GrafanaDashboardLatency
	Period       config.Duration
	Timeout      config.Duration

	Log    telegraf.Logger `toml:"-"`
	acc    telegraf.Accumulator
	client *http.Client
	ctx    context.Context
}

type GrafanaPrometheusResponseDataResult struct {
	Metric map[string]string `json:"metric"`
	Values [][]interface{}   `json:"values"`
}

type GrafanaPrometheusResponseData struct {
	Result     []GrafanaPrometheusResponseDataResult `json:"result"`
	ResultType string                                `json:"resultType"`
}

type GrafanaPrometheusResponse struct {
	Status string                         `json:"status"`
	Data   *GrafanaPrometheusResponseData `json:"data,omitempty"`
}

type GrafanaInfluxDBResponseResultSeria struct {
	Columns []string          `json:"columns"`
	Name    string            `json:"name"`
	Tags    map[string]string `json:"tags,omitempty"`
	Values  [][]interface{}   `json:"values"`
}

type GrafanaInfluxDBResponseResult struct {
	Series []GrafanaInfluxDBResponseResultSeria `json:"series,omitempty"`
}

type GrafanaInfluxDBResponse struct {
	Results []GrafanaInfluxDBResponseResult `json:"results,omitempty"`
}

type GrafanaAlexanderzobninZabbixHostGroupResponseItem struct {
	GroupID string `json:"groupid"`
	Name    string `json:"name"`
}

type GrafanaAlexanderzobninZabbixHostGroupResponse struct {
	Result []*GrafanaAlexanderzobninZabbixHostGroupResponseItem `json:"result,omitempty"`
}

type GrafanaAlexanderzobninZabbixHostGroupRequestParams struct {
	Output    []string `json:"output"`
	RealHosts bool     `json:"real_hosts"`
	SortField string   `json:"sortfield"`
}

type GrafanaAlexanderzobninZabbixHostGroupRequest struct {
	DatasourceId int                                                `json:"datasourceId"`
	Method       string                                             `json:"method"`
	Params       GrafanaAlexanderzobninZabbixHostGroupRequestParams `json:"params"`
}

type GrafanaAlexanderzobninZabbixHostResponseItem struct {
	Host   string `json:"host"`
	HostID string `json:"hostid"`
	Name   string `json:"name"`
}

type GrafanaAlexanderzobninZabbixHostResponse struct {
	Result []*GrafanaAlexanderzobninZabbixHostResponseItem `json:"result,omitempty"`
}

type GrafanaAlexanderzobninZabbixHostRequestParams struct {
	GroupIDs  []string `json:"groupids"`
	Output    []string `json:"output"`
	SortField string   `json:"sortfield"`
}

type GrafanaAlexanderzobninZabbixHostRequest struct {
	DatasourceId int                                           `json:"datasourceId"`
	Method       string                                        `json:"method"`
	Params       GrafanaAlexanderzobninZabbixHostRequestParams `json:"params"`
}

type GrafanaAlexanderzobninZabbixApplicationResponseItem struct {
	ApplicationID string   `json:"applicationid"`
	Flags         string   `json:"flags"`
	HostID        string   `json:"hostid"`
	Name          string   `json:"name"`
	TemplateIDs   []string `json:"templateids,omitempty"`
}

type GrafanaAlexanderzobninZabbixApplicationResponse struct {
	Result []*GrafanaAlexanderzobninZabbixApplicationResponseItem `json:"result,omitempty"`
}

type GrafanaAlexanderzobninZabbixApplicationRequestParams struct {
	HostIDs []string `json:"hostids"`
	Output  string   `json:"output"`
}

type GrafanaAlexanderzobninZabbixApplicationRequest struct {
	DatasourceId int                                                  `json:"datasourceId"`
	Method       string                                               `json:"method"`
	Params       GrafanaAlexanderzobninZabbixApplicationRequestParams `json:"params"`
}

type GrafanaAlexanderzobninZabbixItemResponseItem struct {
	ItemID string `json:"itemid"`
	HostID string `json:"hostid"`
	Name   string `json:"name"`
}

type GrafanaAlexanderzobninZabbixItemResponse struct {
	Result []*GrafanaAlexanderzobninZabbixItemResponseItem `json:"result,omitempty"`
}

type GrafanaAlexanderzobninZabbixItemRequestParams struct {
	ApplicationIDs []string               `json:"applicationds"`
	Filter         map[string]interface{} `json:"filter"`
	Output         []string               `json:"output"`
	//	SelectHosts    []string               `json:"selectHosts"`
	SortField string `json:"sortfield"`
	WebItems  bool   `json:"webitems"`
}

type GrafanaAlexanderzobninZabbixItemRequest struct {
	DatasourceId int                                           `json:"datasourceId"`
	Method       string                                        `json:"method"`
	Params       GrafanaAlexanderzobninZabbixItemRequestParams `json:"params"`
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

func (g *GrafanaDashboard) datasourceJSONValue(ds *sdk.Datasource, key string) string {

	m, ok := ds.JSONData.(map[string]interface{})
	if ok {
		t, ok := m[key].(string)
		if ok {
			return t
		}
	}
	return ""
}

func (g *GrafanaDashboard) datasourceProxyIsPost(ds *sdk.Datasource) bool {

	v := g.datasourceJSONValue(ds, "httpMethod")
	return (v == "POST")
}

func (g *GrafanaDashboard) findAvailability(name string) *GrafanaDashboardAvailability {

	for _, a := range g.Availability {
		for _, s := range a.Panels {
			if b, _ := regexp.MatchString(s, name); b {
				return a
			}
		}
	}
	return nil
}

func (g *GrafanaDashboard) findLatency(name string) *GrafanaDashboardLatency {

	for _, l := range g.Latency {
		for _, s := range l.Panels {
			if b, _ := regexp.MatchString(s, name); b {
				return l
			}
		}
	}
	return nil
}

func (g *GrafanaDashboard) getPeriod(ms *GrafanaDashboardMetrics) (config.Duration, string) {

	period := g.Period
	if ms.Availability != nil {
		if ms.Availability.Period > 0 {
			period = ms.Availability.Period
		}
	} else if ms.Latency != nil {
		if ms.Latency.Period > 0 {
			period = ms.Latency.Period
		}
	}

	periods := time.Duration(period).String()
	return period, periods
}

func (g *GrafanaDashboard) setVariables(vars map[string]string, query string) string {

	s := query
	for k, v := range vars {
		s = strings.ReplaceAll(s, fmt.Sprintf("$%s", k), v)
		s = strings.ReplaceAll(s, fmt.Sprintf("${%s}", k), v)
	}
	return s
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

func (g *GrafanaDashboard) setExtraTags(tags map[string]string, ms *GrafanaDashboardMetrics) {

	if ms.Availability != nil {
		g.setExtraMetricTags(tags, &ms.Availability.GrafanaDashboardMetric)
	}
	if ms.Latency != nil {
		g.setExtraMetricTags(tags, &ms.Latency.GrafanaDashboardMetric)
	}
}

func (g *GrafanaDashboard) httpDoRequest(method, query string, params url.Values, buf io.Reader) ([]byte, int, error) {
	u, _ := url.Parse(g.URL)
	u.Path = path.Join(u.Path, query)
	if params != nil {
		u.RawQuery = params.Encode()
	}
	req, err := http.NewRequest(method, u.String(), buf)
	if err != nil {
		return nil, 0, err
	}
	req = req.WithContext(g.ctx)
	if !strings.Contains(g.APIKey, ":") {
		req.Header.Set("Authorization", "Bearer "+g.APIKey)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	resp, err := g.client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	data, err := ioutil.ReadAll(resp.Body)
	resp.Body.Close()
	return data, resp.StatusCode, err
}

func (g *GrafanaDashboard) httpPost(query string, params url.Values, body []byte) ([]byte, int, error) {
	return g.httpDoRequest("POST", query, params, bytes.NewBuffer(body))
}

func (g *GrafanaDashboard) httpGet(query string, params url.Values) ([]byte, int, error) {
	return g.httpDoRequest("GET", query, params, nil)
}

func (g *GrafanaDashboard) grafanaData(ds *sdk.Datasource, query string, params url.Values, body []byte) ([]byte, int, error) {

	var (
		raw  []byte
		code int
		err  error
	)

	if g.datasourceProxyIsPost(ds) {
		if raw, code, err = g.httpPost(query, params, body); err != nil {
			return raw, code, err
		}
	} else {
		if raw, code, err = g.httpGet(query, params); err != nil {
			g.Log.Error(err)
			return raw, code, err
		}
	}
	return raw, code, err
}

func (g *GrafanaDashboard) setPrometheusData(b *sdk.Board, p *sdk.Panel, ds *sdk.Datasource, t *sdk.Target, ms *GrafanaDashboardMetrics) {

	if ms.Availability == nil && ms.Latency == nil {
		return
	}

	params := make(url.Values)
	params.Add("query", t.Expr)

	period, _ := g.getPeriod(ms)
	start := int(time.Now().UTC().Add(time.Duration(-period)).Unix())
	end := int(time.Now().UTC().Unix())

	params.Add("start", strconv.Itoa(start))
	params.Add("end", strconv.Itoa(end))

	params.Add("step", "60") // where it should be find?
	params.Add("timeout", g.datasourceJSONValue(ds, "queryTimeout"))

	customQueryParameters := g.datasourceJSONValue(ds, "customQueryParameters")
	vls, err := url.ParseQuery(customQueryParameters)
	if err == nil {
		for k, arr := range vls {
			for _, v := range arr {
				params.Add(k, v)
			}
		}
	}

	t1 := time.Now().UTC().UnixMilli()

	URL := fmt.Sprintf("/api/datasources/proxy/%d/api/v1/query_range", ds.ID)
	raw, code, err := g.grafanaData(ds, URL, params, nil)
	if err != nil {
		g.Log.Error(err)
		return
	}
	if code != 200 {
		g.Log.Error(fmt.Errorf("prometheus HTTP error %d: returns %s", code, raw))
		return
	}
	var res GrafanaPrometheusResponse
	err = json.Unmarshal(raw, &res)
	if err != nil {
		g.Log.Error(err)
		return
	}
	if res.Status != "success" {
		g.Log.Error(fmt.Errorf("prometheus status %s", res.Status))
		return
	}
	if res.Data == nil {
		g.Log.Debug("Prometheus has no data")
		return
	}

	for _, d := range res.Data.Result {

		tags := make(map[string]string)
		fields := make(map[string]interface{})

		tags["timestamp"] = strconv.Itoa(int(t1))
		tags["duration_ms"] = strconv.Itoa(int(time.Now().UTC().UnixMilli()) - int(t1))
		tags["status"] = res.Status
		tags["title"] = p.CommonPanel.Title
		tags["datasource_type"] = ds.Type
		tags["datasource_name"] = ds.Name

		for k, m := range d.Metric {
			tags[k] = m
		}
		g.setExtraTags(tags, ms)

		for _, v := range d.Values {
			if len(v) == 2 {

				vt, ok := v[0].(float64)
				if !ok {
					g.Log.Debug("Prometheus data key is not float")
					continue
				}
				ts := int64(vt)

				if ms.Availability != nil {
					vv, ok := v[1].(string)
					if !ok {
						g.Log.Debug("Prometheus data value is not string")
						continue
					}
					if f, err := strconv.ParseFloat(vv, 64); err == nil {
						fields["availability"] = f
						g.acc.AddFields("grafana_dashboard", fields, tags, time.Unix(ts, 0))
					}
				}

				if ms.Latency != nil {
					vv, ok := v[1].(string)
					if !ok {
						g.Log.Debug("Prometheus data value is not string")
						continue
					}
					if f, err := strconv.ParseFloat(vv, 64); err == nil {
						fields["latency"] = f
						g.acc.AddFields("grafana_dashboard", fields, tags, time.Unix(ts, 0))
					}
				}
			}
		}
	}
}

func (g *GrafanaDashboard) setInfluxDBData(sb *sdk.Board, p *sdk.Panel, ds *sdk.Datasource, t *sdk.Target, ms *GrafanaDashboardMetrics) {

	if ms.Availability == nil && ms.Latency == nil {
		return
	}

	params := make(url.Values)
	params.Add("db", *ds.Database)

	vars := make(map[string]string)
	_, periods := g.getPeriod(ms)

	vars["timeFilter"] = fmt.Sprintf("time >= now() - %s", periods)
	params.Add("q", g.setVariables(vars, t.Query))
	params.Add("epoch", "ms")

	t1 := time.Now().UTC().UnixMilli()

	URL := fmt.Sprintf("/api/datasources/proxy/%d/query", ds.ID)
	raw, code, err := g.grafanaData(ds, URL, params, nil)
	if err != nil {
		g.Log.Error(err)
		return
	}
	if code != 200 {
		g.Log.Error(fmt.Errorf("influxdb HTTP error %d: returns %s", code, raw))
		return
	}
	var res GrafanaInfluxDBResponse
	err = json.Unmarshal(raw, &res)
	if err != nil {
		g.Log.Error(err)
		return
	}
	if res.Results == nil {
		g.Log.Debug("InfluxDB has no data")
		return
	}

	for _, r := range res.Results {

		tags := make(map[string]string)
		fields := make(map[string]interface{})

		tags["timestamp"] = strconv.Itoa(int(t1))
		tags["duration_ms"] = strconv.Itoa(int(time.Now().UTC().UnixMilli()) - int(t1))
		tags["title"] = p.CommonPanel.Title
		tags["datasource_type"] = ds.Type
		tags["datasource_name"] = ds.Name

		for _, s := range r.Series {

			for k, t := range s.Tags {
				tags[k] = t
			}
			g.setExtraTags(tags, ms)

			for _, v := range s.Values {
				if len(v) == 2 {

					vt, ok := v[0].(float64)
					if !ok {
						g.Log.Debug("InfluxDB data key is not float")
						continue
					}
					ts := int64(vt)

					vv, ok := v[1].(float64)
					if !ok {
						g.Log.Debug("InfluxDB data value is not float")
						continue
					}

					if ms.Availability != nil {
						fields["availability"] = vv
						g.acc.AddFields("grafana_dashboard", fields, tags, time.UnixMilli(ts))
					}
					if ms.Latency != nil {
						fields["latency"] = vv
						g.acc.AddFields("grafana_dashboard", fields, tags, time.UnixMilli(ts))
					}
				}
			}
		}
	}
}

func (g *GrafanaDashboard) getAlexanderzobninZabbixHostGroupIDs(ds *sdk.Datasource, group interface{}) ([]string, *GrafanaAlexanderzobninZabbixHostGroupResponse, error) {

	filter := ""
	if group != nil {
		mm, ok := group.(map[string]interface{})
		if ok {
			f, ok := mm["filter"].(string)
			if ok && f != "" {
				filter = strings.ReplaceAll(f, "/", "")
			}
		}
	}

	request := GrafanaAlexanderzobninZabbixHostGroupRequest{
		DatasourceId: int(ds.ID),
		Method:       "hostgroup.get",
		Params: GrafanaAlexanderzobninZabbixHostGroupRequestParams{
			Output:    []string{"name"},
			RealHosts: true,
			SortField: "name",
		},
	}

	b, err := json.Marshal(request)
	if err != nil {
		return nil, nil, err
	}

	URL := fmt.Sprintf("/api/datasources/%d/resources/zabbix-api", ds.ID)
	raw, code, err := g.httpPost(URL, nil, b)
	if err != nil {
		return nil, nil, err
	}
	if code != 200 {
		return nil, nil, fmt.Errorf("AlexanderzobninZabbix HTTP error %d: returns %s", code, raw)
	}

	var res GrafanaAlexanderzobninZabbixHostGroupResponse
	err = json.Unmarshal(raw, &res)
	if err != nil {
		return nil, nil, err
	}

	var ret []string
	for _, v := range res.Result {
		if v != nil {
			if m, _ := regexp.MatchString(filter, v.Name); m {
				ret = append(ret, v.GroupID)
			}
		}
	}
	return ret, &res, nil
}

func (g *GrafanaDashboard) getAlexanderzobninZabbixHostIDs(ds *sdk.Datasource, hostGroupIDs []string, host interface{}) ([]string, *GrafanaAlexanderzobninZabbixHostResponse, error) {

	filter := ""
	if host != nil {
		mm, ok := host.(map[string]interface{})
		if ok {
			f, ok := mm["filter"].(string)
			if ok && f != "" {
				filter = strings.ReplaceAll(f, "/", "")
			}
		}
	}

	request := GrafanaAlexanderzobninZabbixHostRequest{
		DatasourceId: int(ds.ID),
		Method:       "host.get",
		Params: GrafanaAlexanderzobninZabbixHostRequestParams{
			GroupIDs:  hostGroupIDs,
			Output:    []string{"name", "host"},
			SortField: "name",
		},
	}

	b, err := json.Marshal(request)
	if err != nil {
		return nil, nil, err
	}

	URL := fmt.Sprintf("/api/datasources/%d/resources/zabbix-api", ds.ID)
	raw, code, err := g.httpPost(URL, nil, b)
	if err != nil {
		return nil, nil, err
	}
	if code != 200 {
		return nil, nil, fmt.Errorf("AlexanderzobninZabbix HTTP error %d: returns %s", code, raw)
	}

	var res GrafanaAlexanderzobninZabbixHostResponse
	err = json.Unmarshal(raw, &res)
	if err != nil {
		return nil, nil, err
	}

	var ret []string
	for _, v := range res.Result {
		if v != nil {
			if m, _ := regexp.MatchString(filter, v.Name); m {
				ret = append(ret, v.HostID)
			}
		}
	}
	return ret, &res, nil
}

func (g *GrafanaDashboard) getAlexanderzobninZabbixApplicationIDs(ds *sdk.Datasource, hostIDs []string, application interface{}) ([]string, *GrafanaAlexanderzobninZabbixApplicationResponse, error) {

	filter := ""
	if application != nil {
		mm, ok := application.(map[string]interface{})
		if ok {
			f, ok := mm["filter"].(string)
			if ok && f != "" {
				filter = strings.ReplaceAll(f, "/", "")
			}
		}
	}

	request := GrafanaAlexanderzobninZabbixApplicationRequest{
		DatasourceId: int(ds.ID),
		Method:       "application.get",
		Params: GrafanaAlexanderzobninZabbixApplicationRequestParams{
			HostIDs: hostIDs,
			Output:  "extend",
		},
	}

	b, err := json.Marshal(request)
	if err != nil {
		return nil, nil, err
	}

	URL := fmt.Sprintf("/api/datasources/%d/resources/zabbix-api", ds.ID)
	raw, code, err := g.httpPost(URL, nil, b)
	if err != nil {
		return nil, nil, err
	}
	if code != 200 {
		return nil, nil, fmt.Errorf("AlexanderzobninZabbix HTTP error %d: returns %s", code, raw)
	}

	var res GrafanaAlexanderzobninZabbixApplicationResponse
	err = json.Unmarshal(raw, &res)
	if err != nil {
		return nil, nil, err
	}

	var ret []string
	for _, v := range res.Result {
		if v != nil {
			if m, _ := regexp.MatchString(filter, v.Name); m {
				ret = append(ret, v.HostID)
			}
		}
	}
	return ret, &res, nil
}

func (g *GrafanaDashboard) getAlexanderzobninZabbixItemIDs(ds *sdk.Datasource, applicationIDs []string, item interface{}) ([]string, *GrafanaAlexanderzobninZabbixItemResponse, error) {

	filter := ""
	if item != nil {
		mm, ok := item.(map[string]interface{})
		if ok {
			f, ok := mm["filter"].(string)
			if ok && f != "" {
				filter = strings.ReplaceAll(f, "/", "")
			}
		}
	}

	filterMap := make(map[string]interface{})
	filterMap["value_type"] = []int{0, 3}

	request := GrafanaAlexanderzobninZabbixItemRequest{
		DatasourceId: int(ds.ID),
		Method:       "item.get",
		Params: GrafanaAlexanderzobninZabbixItemRequestParams{
			ApplicationIDs: applicationIDs,
			Filter:         filterMap,
			Output:         []string{"name", "hostid", "units"},
			//SelectHosts:    []string{"host"},
			SortField: "name",
			WebItems:  true,
		},
	}

	b, err := json.Marshal(request)
	if err != nil {
		return nil, nil, err
	}

	URL := fmt.Sprintf("/api/datasources/%d/resources/zabbix-api", ds.ID)
	raw, code, err := g.httpPost(URL, nil, b)
	if err != nil {
		return nil, nil, err
	}
	if code != 200 {
		return nil, nil, fmt.Errorf("AlexanderzobninZabbix HTTP error %d: returns %s", code, raw)
	}

	var res GrafanaAlexanderzobninZabbixItemResponse
	err = json.Unmarshal(raw, &res)
	if err != nil {
		return nil, nil, err
	}

	var ret []string
	for _, v := range res.Result {
		if v != nil {
			if m, _ := regexp.MatchString(filter, v.Name); m {
				ret = append(ret, v.HostID)
			}
		}
	}
	return ret, &res, nil
}

func (g *GrafanaDashboard) setAlexanderzobninZabbixData(sb *sdk.Board, p *sdk.Panel, ds *sdk.Datasource, t *sdk.Target, ms *GrafanaDashboardMetrics) {

	if ms.Availability == nil && ms.Latency == nil {
		return
	}

	params := make(url.Values)

	t1 := time.Now().UTC().UnixMilli()

	hostGroupIDs, _, err := g.getAlexanderzobninZabbixHostGroupIDs(ds, t.Group)
	if err != nil {
		g.Log.Error(err)
		return
	}
	if len(hostGroupIDs) == 0 {
		g.Log.Debug("AlexanderzobninZabbix has no host group IDs")
	}

	hostIDs, _, err := g.getAlexanderzobninZabbixHostIDs(ds, hostGroupIDs, t.Host)
	if err != nil {
		g.Log.Error(err)
		return
	}
	if len(hostIDs) == 0 {
		g.Log.Debug("AlexanderzobninZabbix has no host IDs")
	}

	applicationIDs, _, err := g.getAlexanderzobninZabbixApplicationIDs(ds, hostIDs, t.Application)
	if err != nil {
		g.Log.Error(err)
		return
	}
	if len(applicationIDs) == 0 {
		g.Log.Debug("AlexanderzobninZabbix has no application IDs")
	}

	itemIDs, _, err := g.getAlexanderzobninZabbixItemIDs(ds, applicationIDs, t.Item)
	if err != nil {
		g.Log.Error(err)
		return
	}
	if len(itemIDs) == 0 {
		g.Log.Debug("AlexanderzobninZabbix has no item IDs")
	}

	URL := fmt.Sprintf("/api/datasources/%d/resources/zabbix-api", ds.ID)
	raw, code, err := g.grafanaData(ds, URL, params, nil)
	if err != nil {
		g.Log.Error(err)
		return
	}
	if code != 200 {
		g.Log.Error(fmt.Errorf("influxdb HTTP error %d: returns %s", code, raw))
		return
	}
	var res GrafanaInfluxDBResponse
	err = json.Unmarshal(raw, &res)
	if err != nil {
		g.Log.Error(err)
		return
	}
	if res.Results == nil {
		g.Log.Debug("InfluxDB has no data")
		return
	}

	for _, r := range res.Results {

		tags := make(map[string]string)
		fields := make(map[string]interface{})

		tags["timestamp"] = strconv.Itoa(int(t1))
		tags["duration_ms"] = strconv.Itoa(int(time.Now().UTC().UnixMilli()) - int(t1))
		tags["title"] = p.CommonPanel.Title
		tags["datasource_type"] = ds.Type
		tags["datasource_name"] = ds.Name

		for _, s := range r.Series {

			for k, t := range s.Tags {
				tags[k] = t
			}
			g.setExtraTags(tags, ms)

			for _, v := range s.Values {
				if len(v) == 2 {

					vt, ok := v[0].(float64)
					if !ok {
						g.Log.Debug("InfluxDB data key is not float")
						continue
					}
					ts := int64(vt)

					vv, ok := v[1].(float64)
					if !ok {
						g.Log.Debug("InfluxDB data value is not float")
						continue
					}

					if ms.Availability != nil {
						fields["availability"] = vv
						g.acc.AddFields("grafana_dashboard", fields, tags, time.UnixMilli(ts))
					}
					if ms.Latency != nil {
						fields["latency"] = vv
						g.acc.AddFields("grafana_dashboard", fields, tags, time.UnixMilli(ts))
					}
				}
			}
		}
	}
}

func (g *GrafanaDashboard) setMarcusolssonJsonData(b *sdk.Board, p *sdk.Panel, ds *sdk.Datasource, t *sdk.Target, ms *GrafanaDashboardMetrics) {

	if ms.Availability == nil && ms.Latency == nil {
		return
	}

	if t.URLPath == nil {
		return
	}
	query, ok := t.URLPath.(string)
	if !ok {
		return
	}

	method := "GET"
	if t.Method != nil {
		m, ok := t.Method.(string)
		if ok {
			method = m
		}
	}

	var body []byte
	if t.Body != nil {
		b, ok := t.Body.(string)
		if ok {
			body = []byte(b)
		}
	}

	params := make(url.Values)
	vars := make(map[string]string)

	period, _ := g.getPeriod(ms)
	start := int(time.Now().UTC().Add(time.Duration(-period)).UnixMilli())
	end := int(time.Now().UTC().UnixMilli())

	vars["__from"] = strconv.Itoa(start)
	vars["__to"] = strconv.Itoa(end)

	if t.Params != nil {

		ps, ok := t.Params.([]interface{})
		if ok {
			for _, v := range ps {

				mm, ok := v.([]interface{})
				if !ok {
					continue
				}
				if len(mm) != 2 {
					continue
				}
				pn, ok := mm[0].(string)
				if !ok {
					continue
				}
				pv, ok := mm[1].(string)
				if !ok {
					continue
				}
				params.Add(pn, g.setVariables(vars, pv))
			}
		}
	}

	t1 := time.Now().UTC().UnixMilli()

	URL := fmt.Sprintf("/api/datasources/proxy/%d%s", ds.ID, query)
	raw, code, err := g.httpDoRequest(method, URL, params, bytes.NewBuffer(body))
	if err != nil {
		g.Log.Error(err)
		return
	}
	if code != 200 {
		g.Log.Error(fmt.Errorf("MarcusolssonJson HTTP error %d: returns %s", code, raw))
		return
	}
	var res map[string]interface{}
	err = json.Unmarshal(raw, &res)
	if err != nil {
		g.Log.Error(err)
		return
	}
	if t.Fields == nil {
		g.Log.Error(fmt.Errorf("MarcusolssonJson has no fields"))
		return
	}

	var times []float64
	var series = make(map[string][]float64)

	fs, ok := t.Fields.([]interface{})
	if !ok {
		return
	}

	for _, v := range fs {
		arr, ok := v.(map[string]interface{})
		if !ok {
			continue
		}
		jsonPath, ok := arr["jsonPath"].(string)
		if !ok {
			continue
		}
		language, ok := arr["language"].(string)
		if !ok {
			continue
		}
		name, ok := arr["name"].(string)
		if !ok {
			continue
		}
		ftype, ok := arr["type"].(string)
		if !ok {
			continue
		}

		if language == "jsonata" {
			expr := jsonata.MustCompile(jsonPath)
			data, err := expr.Eval(res)
			if err != nil {
				g.Log.Error(err)
				return
			}
			d, ok := data.([]interface{})
			if !ok {
				continue
			}

			if ftype == "time" {
				for _, v := range d {
					ts, ok := v.(float64)
					if ok {
						times = append(times, ts)
						continue
					}
					s, ok := v.(string)
					if !ok {
						continue
					}
					t, err := time.Parse(time.RFC3339, s)
					if err == nil {
						ts = float64(t.UTC().UnixMilli())
						times = append(times, ts)
					}
				}
			} else {
				for _, v := range d {
					n, ok := v.(float64)
					if ok {
						series[name] = append(series[name], n)
					}
				}
			}
		}
	}

	if len(times) == 0 {
		g.Log.Debug("MarcusolssonJson has no data")
		return
	}

	for i, t := range times {

		tags := make(map[string]string)
		fields := make(map[string]interface{})

		tags["timestamp"] = strconv.Itoa(int(t1))
		tags["duration_ms"] = strconv.Itoa(int(time.Now().UTC().UnixMilli()) - int(t1))
		tags["title"] = p.CommonPanel.Title
		tags["datasource_type"] = ds.Type
		tags["datasource_name"] = ds.Name

		for k, v := range series {

			tags["alias"] = k
			g.setExtraTags(tags, ms)

			if len(v) > i {

				ts := int64(t)

				if ms.Availability != nil {
					fields["availability"] = v[i]
					g.acc.AddFields("grafana_dashboard", fields, tags, time.UnixMilli(ts))
				}
				if ms.Latency != nil {
					fields["latency"] = v[i]
					g.acc.AddFields("grafana_dashboard", fields, tags, time.UnixMilli(ts))
				}
			}
		}
	}
}

func (g *GrafanaDashboard) setData(b *sdk.Board, p *sdk.Panel, ds *sdk.Datasource, dss []sdk.Datasource, metrics GrafanaDashboardMetrics) {

	if p.GraphPanel != nil {

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

			//	wg.Add(1)

			func(w *sync.WaitGroup, gd string, gds *sdk.Datasource, gt *sdk.Target, gm *GrafanaDashboardMetrics) {

				//defer w.Done()

				switch gd {
				case "prometheus":
					g.setPrometheusData(b, p, gds, gt, gm)
				case "influxdb":
					g.setInfluxDBData(b, p, gds, gt, gm)
				case "alexanderzobnin-zabbix-datasource":
					g.setAlexanderzobninZabbixData(b, p, gds, gt, gm)
				case "marcusolsson-json-datasource":
					g.setMarcusolssonJsonData(b, p, gds, gt, gm)
				default:
					g.Log.Debugf("%s is not implemented yet", gd)
				}
			}(&wg, d.Type, d, &t, &metrics)
		}
		//	wg.Wait()
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

		title := p.CommonPanel.Title
		metrics := GrafanaDashboardMetrics{
			Availability: g.findAvailability(title),
			Latency:      g.findLatency(title),
		}
		g.setData(b, p, ds, dss, metrics)
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

	if len(m.Tags) > 0 {
		m.templates = make(map[string]*template.Template)
	}
	for k, v := range m.Tags {
		m.templates[k] = g.getDefaultTemplate(name, k, v)
	}
}

// Gather is called by telegraf when the plugin is executed on its interval.
// It will call TelnetGather based on the configuration and
// also fill an Accumulator that is supplied.
func (g *GrafanaDashboard) Gather(acc telegraf.Accumulator) error {

	// Set default values
	if g.Period == 0 {
		g.Period = config.Duration(time.Second) * 5
	}
	if g.Timeout == 0 {
		g.Timeout = config.Duration(time.Second) * 5
	}
	g.acc = acc

	for _, a := range g.Availability {
		g.setDefaultMetric("availability", &a.GrafanaDashboardMetric)
	}
	for _, l := range g.Latency {
		g.setDefaultMetric("latency", &l.GrafanaDashboardMetric)
	}

	// Gather data
	err := g.GrafanaGather()
	if err != nil {
		return err
	}

	// Add metrics
	//acc.AddFields("grafana_dashboard", fields, tags)
	return nil
}

func init() {
	inputs.Add("grafana_dashboard", func() telegraf.Input {
		return &GrafanaDashboard{}
	})
}
