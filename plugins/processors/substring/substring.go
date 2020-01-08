package substring

import (
	"github.com/influxdata/telegraf"
	"github.com/influxdata/telegraf/plugins/processors"
)

type Substring struct {
	Field    string `toml:"field"`
	Position int    `toml:"position"`
	Length   int    `toml:"length"`
}

var sampleConfig = `
`

func (p *Substring) SampleConfig() string {
	return sampleConfig
}

func (p *Substring) Description() string {
	return "Cut metrics value that pass through this filter."
}

func (p *Substring) Apply(in ...telegraf.Metric) []telegraf.Metric {

	for _, metric := range in {

		if p.Field == "" || p.Length <= 0 {
			continue
		}

		if fv, ok := metric.GetField(p.Field); ok {

			if fv == nil {
				continue
			}

			value := ""

			switch v := fv.(type) {
			case string:
				value = v
			default:
				continue
			}

			if len(value) > p.Length {
				value = value[p.Position:(p.Position + p.Length)]
			}

			metric.RemoveField(p.Field)
			metric.AddField(p.Field, value)
		}
	}

	return in
}

func init() {

	processors.Add("substring", func() telegraf.Processor {
		return &Substring{}
	})
}
