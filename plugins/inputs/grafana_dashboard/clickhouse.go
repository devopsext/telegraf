package grafana_dashboard

import (
	"encoding/json"
	"fmt"
	"github.com/grafana-tools/sdk"
	"github.com/influxdata/telegraf"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type ClickhouseResponseField struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

type ClickhouseResponse struct {
	Meta       []ClickhouseResponseField `json:"meta"`
	Data       []json.RawMessage         `json:"data"`
	Rows       int                       `json:"rows"`
	Statistics struct {
		Elapsed   float64 `json:"elapsed"`
		RowsRead  int     `json:"rows_read"`
		BytesRead int     `json:"bytes_read"`
	} `json:"statistics"`
}

type Clickhouse struct {
	log     telegraf.Logger
	grafana *Grafana
}

type ResRow struct {
	t     time.Time
	tags  map[string]string
	value float64
}

func mapToString(m map[string]string) string {
	var ss []string
	for key, value := range m {
		ss = append(ss, fmt.Sprintf("%s=%s", key, value))
	}
	return strings.Join(ss, ",")
}

func makeMeta(m []ClickhouseResponseField) map[string]string {
	res := make(map[string]string)
	for _, field := range m {
		res[field.Name] = field.Type
	}
	return res
}

func (c *Clickhouse) makeData(rawData []json.RawMessage, meta map[string]string) (res []ResRow) {
	for _, datum := range rawData {
		var i interface{}
		var ts time.Time
		var value float64
		tags := make(map[string]string)

		err := json.Unmarshal(datum, &i)

		if err != nil {
			c.log.Error(err)
			continue
		}

		d := i.(map[string]interface{})
		for key, v := range d {
			if m, found := meta[key]; found {
				if key == "t" && m == "Int64" {
					st, err := strconv.Atoi(v.(string))
					if err != nil {
						c.log.Error(err)
						continue
					}
					ts = time.UnixMilli(int64(st))
				} else if m == "String" {
					tags[key] = v.(string)
				} else {
					float, err := strconv.ParseFloat(v.(string), 64)
					if err != nil {
						c.log.Error(err)
						continue
					}
					value = float
				}
			}
		}
		res = append(res, ResRow{ts, tags, value})
	}
	return res
}

func (c *Clickhouse) GetData(t *sdk.Target, ds *sdk.Datasource, period *GrafanaDashboardPeriod, push GrafanaDatasourcePushFunc) error {

	t1, t2 := period.StartEnd()
	start := int(t1.UTC().Unix())
	end := int(t2.UTC().Unix())

	vars := make(map[string]string)
	vars["timeFilter"] = fmt.Sprintf("(date >= toDate(%d) and date <= toDate(%d))", start, end)
	vars["from"] = fmt.Sprintf("%d", start)
	vars["to"] = fmt.Sprintf("%d", end)
	vars["table"] = fmt.Sprintf("%s.%s", t.Database, t.Table)

	params := make(url.Values)
	params.Add("query", c.grafana.setVariables(vars, t.Query)+" FORMAT JSON")

	chURL := fmt.Sprintf("/api/datasources/proxy/%d/", ds.ID)

	raw, code, err := c.grafana.getData(ds, chURL, params, nil)
	if err != nil {
		c.log.Error(err)
		return err
	}
	if code != 200 {
		return fmt.Errorf("clickhouse HTTP error %d: returns %s", code, raw)
	}

	var res ClickhouseResponse
	err = json.Unmarshal(raw, &res)
	if err != nil {
		c.log.Error(err)
		return err
	}

	when := time.Now()

	meta := makeMeta(res.Meta)
	data := c.makeData(res.Data, meta)
	for _, datum := range data {
		push(when, datum.tags, datum.t, datum.value)
	}

	return nil
}

func NewClickhouse(log telegraf.Logger, grafana *Grafana) *Clickhouse {
	return &Clickhouse{
		log:     log,
		grafana: grafana,
	}
}
