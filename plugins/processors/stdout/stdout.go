package stdout

import (
	"bytes"
	"fmt"
	"log"
	"text/template"

	"github.com/influxdata/telegraf"
	"github.com/influxdata/telegraf/plugins/processors"
)

type Stdout struct {
	template *template.Template
	Format   string `toml:"format"`
}

var sampleConfig = `
`

func (p *Stdout) SampleConfig() string {
	return sampleConfig
}

func (p *Stdout) Description() string {
	return "Print metrics that pass through this filter using predefined format."
}

func (p *Stdout) Apply(in ...telegraf.Metric) []telegraf.Metric {

	if p.template == nil {

		log.Printf("I! [processors.stdout] format: %s", p.Format)

		t, err := template.New("").Parse(p.Format)
		if err != nil {
			log.Printf("E! [processors.stdout] could not create template: %v", err)
			return in
		}
		p.template = t
	}

	for _, metric := range in {

		m := make(map[string]interface{})
		m["name"] = metric.Name()
		m["time"] = metric.Time()
		m["fields"] = metric.Fields()
		m["tags"] = metric.Tags()

		var b bytes.Buffer
		err := p.template.Execute(&b, m)
		if err == nil {

			fmt.Printf("%s\n", b.String())
		}
	}

	return in
}

func init() {

	processors.Add("stdout", func() telegraf.Processor {
		return &Stdout{}
	})
}
