package grafana_dashboard

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"time"

	"github.com/grafana-tools/sdk"
	"github.com/influxdata/telegraf"
)

type PrometheusResponseDataResult struct {
	Metric map[string]string `json:"metric"`
	Values [][]interface{}   `json:"values"`
}

type PrometheusResponseData struct {
	Result     []PrometheusResponseDataResult `json:"result"`
	ResultType string                         `json:"resultType"`
}

type PrometheusResponse struct {
	Status string                  `json:"status"`
	Data   *PrometheusResponseData `json:"data,omitempty"`
}

type Prometheus struct {
	log     telegraf.Logger
	grafana *Grafana
}

func (gp *Prometheus) GetData(t *sdk.Target, ds *sdk.Datasource, period *GrafanaDashboardPeriod, push GrafanaDatasourcePushFunc) error {
	const gStep int64 = 60
	const maxPoints int64 = 1000

	params := make(url.Values)
	params.Add("query", t.Expr)

	t1, t2 := period.StartEnd()
	start := t1.UTC().Unix()
	end := t2.UTC().Unix()

	pStep := gStep * maxPoints

	params.Add("start", strconv.FormatInt(start, 10))
	params.Add("end", strconv.FormatInt(end, 10))

	params.Add("step", strconv.FormatInt(gStep, 10)) // where it should be found?
	params.Add("timeout", gp.grafana.datasourceJSONValue(ds, "queryTimeout"))

	customQueryParameters := gp.grafana.datasourceJSONValue(ds, "customQueryParameters")
	vls, err := url.ParseQuery(customQueryParameters)
	if err == nil {
		for k, arr := range vls {
			for _, v := range arr {
				params.Add(k, v)
			}
		}
	}

	for i := start; i < end; i += pStep {
		s := i
		e := i + pStep
		if e > end {
			e = end
		}
		params.Set("start", strconv.FormatInt(s, 10))
		params.Set("end", strconv.FormatInt(e, 10))

		gp.log.Debugf("querying prometheus from %s to %s", time.Unix(s, 0).Format(time.RFC3339), time.Unix(e, 0).Format(time.RFC3339))

		// gp.log.Debugf("Prometheus request params => %s", string(params))

		when := time.Now()

		gURL := fmt.Sprintf("/api/datasources/proxy/%d/api/v1/query_range", ds.ID)
		raw, code, err := gp.grafana.getData(ds, gURL, params, nil)
		if err != nil {
			return err
		}
		if code != 200 {
			return fmt.Errorf("prometheus HTTP error %d: returns %s", code, raw)
		}
		var res PrometheusResponse
		err = json.Unmarshal(raw, &res)
		if err != nil {
			return err
		}
		if res.Status != "success" {
			return fmt.Errorf("prometheus status %s", res.Status)
		}
		if res.Data == nil {
			gp.log.Debug("Prometheus has no data")
			return nil
		}

		for _, d := range res.Data.Result {
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
		time.Sleep(2 * time.Second)
	}

	return nil
}

func NewPrometheus(log telegraf.Logger, grafana *Grafana) *Prometheus {
	return &Prometheus{
		log:     log,
		grafana: grafana,
	}
}
