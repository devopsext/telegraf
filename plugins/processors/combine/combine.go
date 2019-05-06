package combine

import (
	"bytes"
	"github.com/influxdata/telegraf"
	"github.com/influxdata/telegraf/plugins/processors"
	"log"
	"text/template"
)

type Combine struct {
	template *template.Template
	Format   string `toml:"format"`
	DestType string `toml:"destType"` // field, tag
	Dest 	 string `toml:"dest"` //name of destination object
	init     bool

}

var sampleConfig = `
#[[processors.combine]]
#  format = "{{ index .tags \"source\"}}|{{index .tags \"process\"}}" 
#  #Also available:
#  # {{.name}} - metric name
#  # {{.time}} - metric timestamp
#  # {{index .fields "filedName"}} - access to metric fields
#  # {{index .tags "tagName"}} - access to metric tags
#  destType = "tag" # destination type - 'tag' or 'field'
#  dest = "myNewTag" # name of the destination tag or field. If exist, will be overwritten
`

func (p *Combine) SampleConfig() string {
	return sampleConfig
}

func (p *Combine) Description() string {
	return "Build new/replace existing tags and fields based on	metric: name, timestamp, tags, fields. The output described in a form of go template."
}

func (p *Combine) Apply(in ...telegraf.Metric) []telegraf.Metric {

	if ! p.initOnce() {return in}

	for _, metric := range in {
		m := make(map[string]interface{})
		m["name"] = metric.Name()
		m["time"] = metric.Time()
		m["fields"] = metric.Fields()
		m["tags"] = metric.Tags()

		var b bytes.Buffer
		err := p.template.Execute(&b, m)
		if err == nil {
			//fmt.Printf("%s\n", b.String())
			if p.DestType == "tag" {
				metric.AddTag(p.Dest, b.String())
			}else if p.DestType == "field" {
				metric.AddField(p.Dest, b.String())
			}

		}else {
			log.Printf("E! [processors.combine] could not render template: %v", err)
		}
	}

	return in
}

func (p *Combine) initOnce() bool {
	if p.init {return true}

	if p.template == nil { //Initialization

		log.Printf("I! [processors.combine] format: %s", p.Format)

		t, err := template.New("").Parse(p.Format)
		if err != nil {
			log.Printf("E! [processors.combine] could not create template: %v", err)
			return false
		}
		p.template = t
	}

	//Checking validity of dest type
	if p.DestType != "field" && p.DestType != "tag" {
		log.Printf("E! [processors.combine] unsupported destination type: '%s'. Supported values are: 'tag', 'field'", p.DestType)
		return false
	}

	p.init = true

	return true
}

func init() {

	processors.Add("combine", func() telegraf.Processor {
		return &Combine{}
	})
}
