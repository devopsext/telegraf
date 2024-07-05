package prometheus_http

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"time"

	utils "github.com/devopsext/utils"
	"github.com/influxdata/telegraf"
)

type PrometheusHttpV1ResponseDataResult struct {
	Metric map[string]string `json:"metric"`
	Value  []interface{}     `json:"value"`
	Values [][]interface{}   `json:"values"`
}

type PrometheusHttpV1ResponseData struct {
	Result     []PrometheusHttpV1ResponseDataResult `json:"result"`
	ResultType string                               `json:"resultType"`
}

type PrometheusHttpV1Response struct {
	Status string                        `json:"status"`
	Data   *PrometheusHttpV1ResponseData `json:"data,omitempty"`
}

type PrometheusHttpV1 struct {
	log      telegraf.Logger
	ctx      context.Context
	client   *http.Client
	name     string
	url      string
	user     string
	password string
	step     string
	params   string
	close    bool
}

func (p *PrometheusHttpV1) httpDoRequest(method, query string, params url.Values, buf io.Reader) ([]byte, int, error) {

	u, _ := url.Parse(p.url)
	u.Path = path.Join(u.Path, query)
	if params != nil {
		u.RawQuery = params.Encode()
	}
	req, err := http.NewRequest(method, u.String(), buf)
	if err != nil {
		return nil, 0, err
	}

	ctx, cancel := context.WithTimeout(p.ctx, time.Duration(time.Millisecond*p.client.Timeout))
	defer cancel()

	req = req.WithContext(ctx)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")

	if p.close {
		req.Header.Set("Connection", "close")
	}

	if p.user != "" || p.password != "" {
		req.SetBasicAuth(p.user, p.password)
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	return data, resp.StatusCode, err
}

func (p *PrometheusHttpV1) getQueryRangeData(query string, period *PrometheusHttpPeriod, params *url.Values) string {

	path := "/api/v1/query_range"

	t1, t2 := period.StartEnd()
	start := int(t1.UTC().Unix())
	end := int(t2.UTC().Unix())

	params.Add("start", strconv.Itoa(start))
	params.Add("end", strconv.Itoa(end))
	params.Add("step", p.step)

	return path
}

func (p *PrometheusHttpV1) processMatrix(res *PrometheusHttpV1Response, when time.Time, push PrometheusHttpPushFunc) {

	for _, d := range res.Data.Result {

		if len(d.Values) == 0 {
			continue
		}

		tags := make(map[string]string)
		for k, m := range d.Metric {
			tags[k] = m
		}

		for _, v := range d.Values {
			if len(v) == 2 {

				vt, ok := v[0].(float64)
				if !ok {
					continue
				}
				ts := int64(vt)

				vv, ok := v[1].(string)
				if !ok {
					continue
				}
				if f, err := strconv.ParseFloat(vv, 64); err == nil {
					push(when, tags, time.Unix(ts, 0), f)
				}
			}
		}
	}
}

func (p *PrometheusHttpV1) processVector(res *PrometheusHttpV1Response, when time.Time, push PrometheusHttpPushFunc) {

	for _, d := range res.Data.Result {

		if len(d.Value) != 2 {
			continue
		}

		tags := make(map[string]string)
		for k, m := range d.Metric {
			tags[k] = m
		}

		vt, ok := d.Value[0].(float64)
		if !ok {
			continue
		}
		ts := int64(vt)

		vv, ok := d.Value[1].(string)
		if !ok {
			continue
		}
		if f, err := strconv.ParseFloat(vv, 64); err == nil {
			push(when, tags, time.Unix(ts, 0), f)
		}
	}
}

func (p *PrometheusHttpV1) GetData(query string, period *PrometheusHttpPeriod, push PrometheusHttpPushFunc) (*PrometheusHttpDatasourceResponse, error) {

	gid := utils.GoRoutineID()

	params := make(url.Values)
	params.Add("query", query)

	path := "/api/v1/query"
	duration := period.Duration()
	if duration != 0 {
		path = p.getQueryRangeData(query, period, &params)
	}

	vls, err := url.ParseQuery(p.params)
	if err == nil {
		for k, arr := range vls {
			for _, v := range arr {
				params.Add(k, v)
			}
		}
	}

	when := time.Now()

	dr := &PrometheusHttpDatasourceResponse{}

	raw, code, err := p.httpDoRequest("GET", path, params, nil)
	if err != nil {
		dr.request = time.Since(when)
		return dr, fmt.Errorf("[%d] %s prometheus HTTP error %s [%s]", gid, p.name, err, time.Since(when))
	}

	if code != 200 {
		dr.request = time.Since(when)
		return dr, fmt.Errorf("[%d] %s prometheus HTTP error %d: returns %s [%s]", gid, p.name, code, raw, time.Since(when))
	}

	dr.request = time.Since(when)
	when = time.Now()

	var res PrometheusHttpV1Response
	err = json.Unmarshal(raw, &res)
	if err != nil {
		dr.unmarshal = time.Since(when)
		return dr, fmt.Errorf("[%d] %s prometheus unmarshall error %s [%s]", gid, p.name, err, time.Since(when))
	}
	if res.Status != "success" {
		dr.unmarshal = time.Since(when)
		return dr, fmt.Errorf("[%d] %s prometheus status %s [%s]", gid, p.name, res.Status, time.Since(when))
	}
	if res.Data == nil {
		dr.unmarshal = time.Since(when)
		return dr, nil
	}

	dr.unmarshal = time.Since(when)
	when = time.Now()

	switch res.Data.ResultType {
	case "matrix":
		p.processMatrix(&res, when, push)
	case "vector":
		p.processVector(&res, when, push)
	default:
		dr.process = time.Since(when)
		return dr, fmt.Errorf("[%d] %s prometheus result type %s is not supported [%s]", gid, p.name, res.Data.ResultType, time.Since(when))
	}
	dr.process = time.Since(when)
	return dr, nil
}

func NewPrometheusHttpV1(client *http.Client, name string, log telegraf.Logger, ctx context.Context, url string, user string, password string, timeout int, step string, params string) *PrometheusHttpV1 {

	close := false
	if client == nil {

		t := time.Duration(timeout)

		var transport = &http.Transport{
			Dial:                (&net.Dialer{}).Dial,
			TLSHandshakeTimeout: t,
			TLSClientConfig:     &tls.Config{InsecureSkipVerify: true},
			IdleConnTimeout:     t,
		}

		client = &http.Client{
			Timeout:   t,
			Transport: transport,
		}
		close = true
	}

	if step == "" {
		step = "60"
	}

	return &PrometheusHttpV1{
		name:     name,
		log:      log,
		ctx:      ctx,
		client:   client,
		url:      url,
		user:     user,
		password: password,
		step:     step,
		params:   params,
		close:    close,
	}
}
