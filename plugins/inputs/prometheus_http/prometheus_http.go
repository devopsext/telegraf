package prometheus_http

import (
	"context"
	"crypto/sha512"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"math"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/allegro/bigcache"
	"github.com/araddon/dateparse"
	"gopkg.in/yaml.v3"

	"github.com/influxdata/telegraf"
	"github.com/influxdata/telegraf/config"
	"github.com/influxdata/telegraf/plugins/inputs"

	toolsRender "github.com/devopsext/tools/render"
	utils "github.com/devopsext/utils"
)

type PrometheusHttpTextTemplate struct {
	template *toolsRender.TextTemplate
	input    *PrometheusHttp
	metric   *PrometheusHttpMetric
	name     string
	tag      string
	value    string
	//hash     uint64
}

// PrometheusHttpMetric struct
type PrometheusHttpMetric struct {
	Name        string `toml:"name"`
	Query       string `toml:"query"`
	Transform   string `toml:"transform"`
	Round       *int   `toml:"round"`
	template    *toolsRender.TextTemplate
	Duration    config.Duration   `toml:"duration"`
	From        string            `toml:"from"`
	Step        string            `toml:"step"`
	Params      string            `toml:"params"`
	Timeout     config.Duration   `toml:"timeout"`
	Interval    config.Duration   `toml:"interval"`
	Tags        map[string]string `toml:"tags"`
	UniqueBy    []string          `toml:"unique_by"`
	templates   map[string]*toolsRender.TextTemplate
	dependecies map[string][]string
	only        map[string]string
	uniques     map[uint64]bool
	//cacheKeys   map[uint64]string
}

// PrometheusHttpFile
type PrometheusHttpFile struct {
	Name string `toml:"name"`
	Path string `toml:"path"`
	Type string `toml:"type"`
}

// PrometheusHttpPeriod struct
type PrometheusHttpPeriod struct {
	duration config.Duration
	from     string
}

// PrometheusHttp struct
type PrometheusHttp struct {
	Name          string                  `toml:"name"`
	URL           string                  `toml:"url"`
	User          string                  `toml:"user"`
	Password      string                  `toml:"password"`
	Metrics       []*PrometheusHttpMetric `toml:"metric"`
	Duration      config.Duration         `toml:"duration"`
	Interval      config.Duration         `toml:"interval"`
	From          string                  `toml:"from"`
	Timeout       config.Duration         `toml:"timeout"`
	Version       string                  `toml:"version"`
	Step          string                  `toml:"step"`
	Params        string                  `toml:"params"`
	Prefix        string                  `toml:"prefix"`
	SkipEmptyTags bool                    `toml:"skip_empty_tags"`
	CacheDuration config.Duration         `toml:"cache_duration"`
	CacheSize     config.Size             `toml:"cache_size"`
	Files         []*PrometheusHttpFile   `toml:"file"`

	Log telegraf.Logger `toml:"-"`
	acc telegraf.Accumulator

	requests *RateCounter
	errors   *RateCounter
	client   *http.Client
	mtx      *sync.Mutex
	//files    *sync.Map
	//fileHash map[string]string
	cache *bigcache.BigCache
}

type PrometheusHttpPushFunc = func(when time.Time, tags map[string]string, stamp time.Time, value float64)

type PrometheusHttpDatasourceResponse struct {
	request   time.Duration
	unmarshal time.Duration
	process   time.Duration
}

type PrometheusHttpDatasource interface {
	GetData(query string, period *PrometheusHttpPeriod, push PrometheusHttpPushFunc) (*PrometheusHttpDatasourceResponse, error)
}

var description = "Collect data from Prometheus http api"

var globalFiles = sync.Map{}
var globalHashes = sync.Map{}

const pluginName = "prometheus_http"

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

func (p *PrometheusHttp) Info(obj interface{}, args ...interface{}) {
	s, ok := obj.(string)
	if !ok {
		return
	}
	p.Log.Infof(s, args)
}

func (p *PrometheusHttp) Warn(obj interface{}, args ...interface{}) {
	s, ok := obj.(string)
	if !ok {
		return
	}
	p.Log.Warnf(s, args)
}

func (p *PrometheusHttp) Debug(obj interface{}, args ...interface{}) {
	s, ok := obj.(string)
	if !ok {
		return
	}
	p.Log.Debugf(s, args)
}

func (p *PrometheusHttp) Error(obj interface{}, args ...interface{}) {
	s, ok := obj.(string)
	if !ok {
		return
	}
	p.Log.Errorf(s, args)
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

func (p *PrometheusHttp) getTemplateValue(t *toolsRender.TextTemplate, value float64) (float64, error) {

	if t == nil {
		return value, nil
	}

	b, err := t.RenderObject(value)
	if err != nil {
		return value, err
	}
	v := strings.TrimSpace(string(b))

	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return value, err
	}
	return f, nil
}

/*func (p *PrometheusHttp) fRenderMetricTag(template string, obj interface{}) interface{} {

	t, err := toolsRender.NewTextTemplate(toolsRender.TemplateOptions{
		Content:     template,
		FilterFuncs: true,
	}, p)
	if err != nil {
		p.Log.Error(err)
		return err
	}

	b, err := t.RenderObject(obj)
	if err != nil {
		p.Log.Error(err)
		return err
	}
	return string(b)
}*/

func (p *PrometheusHttp) getAllTags(values, metricTags, metricVars map[string]string) map[string]interface{} {

	tgs := make(map[string]interface{})
	for k, v := range values {
		tgs[k] = v
	}

	m := tgs
	m["values"] = values
	m["tags"] = metricTags
	m["vars"] = metricVars

	files := make(map[string]interface{})

	globalFiles.Range(func(key, value interface{}) bool {
		files[fmt.Sprint(key)] = value
		return true
	})
	m["files"] = files
	return m
}

func (p *PrometheusHttp) setExtraMetricTag(gid uint64, t *toolsRender.TextTemplate, values, metricTags, metricVars map[string]string) (string, error) {

	m := p.getAllTags(values, metricTags, metricVars)
	b, err := t.RenderObject(&m)
	if err != nil {
		p.Log.Errorf("[%d] %s failed to execute template: %v", gid, p.Name, err)
		return "", err
	}
	r := strings.TrimSpace(string(b))
	// simplify <no value> => empty string
	return strings.ReplaceAll(r, "<no value>", ""), nil
}

func (p *PrometheusHttp) getOnly(gid uint64, only string, values, metricTags, metricVars map[string]string) string {

	e := "error"
	m := p.getAllTags(values, metricTags, metricVars)

	arr := strings.FieldsFunc(only, func(c rune) bool {
		return c == '.'
	})

	l := len(arr)
	switch l {
	case 0:
		p.Log.Errorf("[%d] %s no dots for: %s", gid, p.Name, only)
		return e
	case 1:
		v, ok := m["values"].(map[string]string)
		if ok {
			return v[arr[0]]
		}
	case 2:
		v, ok := m[arr[0]].(map[string]string)
		if ok {
			return v[arr[1]]
		}
	default:
		p.Log.Errorf("[%d] %s dots are more than two: %s", gid, p.Name, only)
		return e
	}
	return e
}

func (p *PrometheusHttp) getIntKeys(arr map[string]int) []string {
	var keys []string
	for k := range arr {
		keys = append(keys, k)
	}
	return keys
}

// func (p *PrometheusHttp) getStringKeys(arr map[string]string) []string {
// 	var keys []string
// 	for k := range arr {
// 		keys = append(keys, k)
// 	}
// 	return keys
// }

func (p *PrometheusHttp) countDeps(key string, deps map[string][]string) int {

	r := 0
	for k, l := range deps {
		if !utils.Contains(l, key) {
			continue
		}
		c := p.countDeps(k, deps)
		r = r + 1 + c
	}
	// for _, k := range tags {
	// 	if len(deps[k]) == 0 || key == k {
	// 		continue
	// 	}
	// 	r = r + p.countDeps(key, deps, deps[k])
	// }
	return r
}

func (p *PrometheusHttp) sortMetricTags(m *PrometheusHttpMetric) []string {

	mm := make(map[string]int)
	for k := range m.Tags {

		mm[k] = mm[k] + p.countDeps(k, m.dependecies)
	}

	kall := p.getIntKeys(mm)
	sort.SliceStable(kall, func(i, j int) bool {

		ki := kall[i]
		kj := kall[j]
		return mm[ki] > mm[kj]
	})

	return kall
}

func (p *PrometheusHttp) getExtraMetricTags(gid uint64, values map[string]string, m *PrometheusHttpMetric) map[string]string {

	if m.templates == nil {
		return values
	}

	vars := make(map[string]string)
	mTags := p.sortMetricTags(m)
	for _, k := range mTags {

		tpl := m.templates[k]
		if tpl != nil {
			vk, err := p.setExtraMetricTag(gid, tpl, values, m.Tags, vars)
			if err != nil {
				vars[k] = "error"
				continue
			}
			vars[k] = vk
			if !p.SkipEmptyTags && vars[k] == "" {
				vars[k] = m.Tags[k]
			}
		} else {
			only := m.only[k]
			if only != "" {
				vars[k] = p.getOnly(gid, only, values, m.Tags, vars)
			} else {
				vars[k] = m.Tags[k]
			}
		}
	}
	return vars
}

func byteHash64(b []byte) uint64 {
	h := fnv.New64()
	h.Write(b)
	return h.Sum64()
}

func byteSha512(b []byte) []byte {
	hasher := sha512.New()
	hasher.Write(b)
	return hasher.Sum(nil)
}

func (p *PrometheusHttp) uniqueHash(pm *PrometheusHttpMetric, tgs map[string]string, stamp time.Time) uint64 {

	if len(pm.UniqueBy) == 0 {
		return 0
	}

	if len(tgs) == 0 {
		return 0
	}

	s := ""
	for _, t := range pm.UniqueBy {
		v1 := ""
		flag := false
		for k, v := range tgs {
			if k == t {
				v1 = v
				flag = true
				break
			}
		}
		if flag {
			s1 := fmt.Sprintf("%s=%s", t, v1)
			if s == "" {
				s = s1
			} else {
				s = fmt.Sprintf("%s,%s", s, s1)
			}
		}
	}

	if s == "" {
		return 0
	}

	hash := fmt.Sprintf("%d:%s", stamp.UnixNano(), s)
	return byteHash64(byteSha512([]byte(hash)))
}

func (p *PrometheusHttp) addFields(name string, value interface{}) map[string]interface{} {

	m := make(map[string]interface{})
	m[name] = value
	return m
}

func (p *PrometheusHttp) makeClient(timeout int) *http.Client {

	t := time.Duration(timeout)

	var transport = &http.Transport{
		Dial:                (&net.Dialer{}).Dial,
		TLSHandshakeTimeout: t,
		TLSClientConfig:     &tls.Config{InsecureSkipVerify: true},
		IdleConnTimeout:     t,
		MaxIdleConns:        8,
		MaxConnsPerHost:     8,
	}

	p.client = &http.Client{
		Timeout:   t,
		Transport: transport,
	}

	return p.client
}

func (p *PrometheusHttp) setMetrics(w *sync.WaitGroup, pm *PrometheusHttpMetric,
	ds PrometheusHttpDatasource, callback func(*PrometheusHttpDatasourceResponse, error)) {

	gid := utils.GoRoutineID()
	//p.Log.Debugf("[%d] %s start gathering %s...", gid, p.Name, pm.Name)

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

		hash := p.uniqueHash(pm, tgs, stamp)
		if hash > 0 {
			if pm.uniques[hash] {
				return
			} else {
				pm.uniques[hash] = true
			}
		}

		v, err := p.getTemplateValue(pm.template, value)
		if err != nil {
			p.Log.Error(err)
			return
		}

		tags := make(map[string]string)

		//millis := when.UTC().UnixMilli()
		//tags["timestamp"] = strconv.Itoa(int(millis))
		//tags["duration_ms"] = strconv.Itoa(int(time.Now().UTC().UnixMilli()) - int(millis))

		for k, t := range tgs {
			if p.SkipEmptyTags && t == "" {
				continue
			}
			tags[k] = t
		}

		tags = p.getExtraMetricTags(gid, tags, pm)

		if math.IsNaN(v) || math.IsInf(v, 0) {
			bs, _ := json.Marshal(tags)
			p.Log.Debugf("[%d] %s skipped NaN/Inf value for: %v[%v]", gid, p.Name, pm.Name, string(bs))
			return
		}

		if pm.Round != nil {
			ratio := math.Pow(10, float64(*pm.Round))
			v = math.Round(v*ratio) / ratio
		}
		p.acc.AddFields(p.Prefix, p.addFields(pm.Name, v), tags, stamp)
	}

	if ds == nil {
		switch p.Version {
		case "v1":

			if p.mtx.TryLock() {

				if p.client == nil {
					p.client = p.makeClient(int(timeout))
				}

				ds = NewPrometheusHttpV1(p.client, p.Name, p.Log, context.Background(), p.URL, p.User, p.Password, int(timeout), step, params)
				p.mtx.Unlock()
			}
		}
	}

	if ds != nil {
		period := p.getMetricPeriod(pm)
		callback(ds.GetData(pm.Query, period, push))
	}
}

func (p *PrometheusHttp) gatherMetrics(gid uint64, ds PrometheusHttpDatasource) error {

	var wg sync.WaitGroup

	tags := make(map[string]string)
	tags[fmt.Sprintf("%s_name", pluginName)] = p.Name
	tags[fmt.Sprintf("%s_url", pluginName)] = p.URL

	when := time.Now()

	for _, m := range p.Metrics {

		if m.Name == "" {
			err := fmt.Errorf("[%d] %s no metric name found", gid, p.Name)
			p.Log.Error(err)
			return err
		}

		wg.Add(1)

		go p.setMetrics(&wg, m, ds, func(dr *PrometheusHttpDatasourceResponse, err error) {

			p.requests.Incr(1)

			if dr != nil {
				p.Log.Debugf("[%d] %s %s request: %s, umarshal: %s, process: %s", gid, p.Name, m.Name, dr.request, dr.unmarshal, dr.process)
			}

			if err != nil {
				p.errors.Incr(1)
				p.Log.Error(err)
			}
		})
	}
	wg.Wait()

	p.Log.Debugf("[%d] %s gathering finished [%s]", gid, p.Name, time.Since(when))

	// availability = (requests - errors) / requests * 100
	// availability = (100 - 0) / 100 * 100 = 100%
	// availability = (100 - 1) / 100 * 100 = 99%
	// availability = (100 - 10) / 100 * 100 = 90%
	// availability = (100 - 100) / 100 * 100 = 0%

	fields := p.addFields("requests", p.requests.counter.Value())
	fields["errors"] = p.errors.counter.Value()

	r1 := float64(p.requests.counter.Value())
	r2 := float64(p.errors.counter.Value())
	if r1 > 0 {
		fields["availability"] = (r1 - r2) / r1 * 100
	}
	p.acc.AddFields(pluginName, fields, tags, time.Now())

	return nil
}

func (ptt *PrometheusHttpTextTemplate) fCacheRegexMatchFindKey(obj interface{}, field, value string) string {

	if obj == nil || utils.IsEmpty(field) || utils.IsEmpty(value) {
		return ""
	}
	if ptt.input.cache == nil {
		return ""
	}
	key := fmt.Sprintf("%s.%s.%s.%s", ptt.name, ptt.tag, field, value)

	entry, err := ptt.input.cache.Get(key)
	if err == nil {
		v1 := string(entry)
		if !utils.IsEmpty(v1) {
			return v1
		}
	}

	v2 := ptt.template.RegexMatchFindKey(obj, field, value)
	if !utils.IsEmpty(v2) {
		v1 := fmt.Sprintf("%v", v2)
		ptt.input.cache.Set(key, []byte(v1))
		return v1
	}
	return ""
}

func (ptt *PrometheusHttpTextTemplate) fCacheRegexMatchObjectByField(obj interface{}, field, value string) interface{} {

	if obj == nil {
		return nil
	}
	if ptt.input.cache == nil {
		return ""
	}
	key := ptt.fCacheRegexMatchFindKey(obj, field, value)
	if utils.IsEmpty(key) {
		return nil
	}

	a, ok := obj.([]interface{})
	ka, err := strconv.Atoi(key)
	if ok && err == nil {
		return a[ka]
	}

	m, ok := obj.(map[string]interface{})
	if ok {
		return m[key]
	}
	return nil
}

func (p *PrometheusHttp) getDefaultTemplate(m *PrometheusHttpMetric, name, tag, value string) *toolsRender.TextTemplate {

	if value == "" {
		return nil
	}
	ptt := &PrometheusHttpTextTemplate{}

	funcs := make(map[string]any)
	//funcs["renderMetricTag"] = p.fRenderMetricTag
	funcs["regexMatchFindKey"] = ptt.fCacheRegexMatchFindKey
	funcs["regexMatchObjectByField"] = ptt.fCacheRegexMatchObjectByField

	tpl, err := toolsRender.NewTextTemplate(toolsRender.TemplateOptions{
		Name:        fmt.Sprintf("%s_template", fmt.Sprintf("%s_%s", name, tag)),
		Content:     value,
		Funcs:       funcs,
		FilterFuncs: true,
	}, p)

	if err != nil {
		p.Log.Error(err)
		return nil
	}
	ptt.template = tpl
	ptt.input = p
	ptt.name = name
	ptt.tag = tag
	ptt.value = value
	ptt.metric = m
	//ptt.hash = byteHash64(byteSha512([]byte(m.Query)))
	return tpl
}

func (p *PrometheusHttp) ifTemplate(s string) (bool, string) {

	only := ""
	if strings.TrimSpace(s) == "" {
		return false, ""
	}
	// find {{ }} to pass templates
	l := len("{{")
	idx1 := strings.Index(s, "{{")
	if idx1 == -1 {
		return false, ""
	}
	s1 := s[idx1+l:]
	idx2 := strings.LastIndex(s1, "}}")
	if idx2 == -1 {
		return false, ""
	}
	s2 := strings.TrimSpace(s1[0:idx2])
	if !utils.IsEmpty(s2) {
		idx3 := strings.Index(s2, "}}")
		if idx3 > -1 {
			return true, ""
		}
	}
	arr := strings.Split(s2, " ")
	if len(arr) == 1 {
		if idx1 == 0 && strings.HasPrefix(arr[0], ".") {
			only = arr[0]
		}
	}
	return true, only
}

func (p *PrometheusHttp) findTagsOnVars(ident, name, value string, tags map[string]string, stack []string) []string {

	var r []string
	if len(tags) == 0 {
		return r
	}
	for k, v := range tags {
		pattern := fmt.Sprintf(".vars.%s", k)
		if strings.Contains(value, pattern) && !utils.Contains(r, k) {
			if utils.Contains(stack, k) {
				return append(r, k)
			}
			r = append(r, k)
			d := p.findTagsOnVars(ident, v, k, tags, append(stack, r...))
			if len(d) > 0 {
				for _, k1 := range d {
					if !utils.Contains(r, k1) {
						r = append(r, k1)
					}
				}
			}
		}
	}
	return r
}

func (p *PrometheusHttp) setDefaultMetric(gid uint64, m *PrometheusHttpMetric) {

	if m.Name == "" {
		return
	}
	if m.Transform != "" {
		m.template = p.getDefaultTemplate(m, m.Name, "", m.Transform)
	}
	if len(m.Tags) > 0 {
		m.templates = make(map[string]*toolsRender.TextTemplate)
	}
	m.dependecies = make(map[string][]string)
	m.only = make(map[string]string)
	for k, v := range m.Tags {

		b, only := p.ifTemplate(v)
		if b {

			d := p.findTagsOnVars(m.Name, k, v, m.Tags, []string{k})
			if utils.Contains(d, k) {
				p.Log.Errorf("[%d] %s metric %s: %s dependency contains in %s", gid, p.Name, m.Name, k, d)
				continue
			}
			if len(d) > 0 {
				p.Log.Debugf("[%d] %s metric %s %s dependencies are %s", gid, p.Name, m.Name, k, d)
			}
			m.dependecies[k] = d
			if only == "" {
				m.templates[k] = p.getDefaultTemplate(m, m.Name, k, v)
			} else {
				m.only[k] = only
			}
		}
	}
	if m.uniques == nil {
		m.uniques = make(map[uint64]bool)
	}
}

func (p *PrometheusHttp) readJson(bytes []byte) (interface{}, error) {

	var v interface{}
	err := json.Unmarshal(bytes, &v)
	if err != nil {
		return nil, err
	}
	return v, nil
}

func (p *PrometheusHttp) readToml(bytes []byte) (interface{}, error) {

	return nil, fmt.Errorf("toml is not implemented")
}

func (p *PrometheusHttp) readYaml(bytes []byte) (interface{}, error) {

	var v interface{}
	err := yaml.Unmarshal(bytes, &v)
	if err != nil {
		return nil, err
	}
	return v, nil
}

func (p *PrometheusHttp) fileInUse(name string, list []*PrometheusHttpMetric) bool {

	pattern := fmt.Sprintf(".files.%s", name)
	for _, m := range list {
		for k, v := range m.Tags {

			t := m.templates[k]

			if t != nil && !utils.IsEmpty(v) && strings.Contains(v, pattern) {
				return true
			}
		}
	}
	return false
}

func (p *PrometheusHttp) objectHash(obj interface{}) uint64 {

	if obj == nil {
		return 0
	}

	bytes, err := json.Marshal(obj)
	if err != nil {
		return 0
	}
	return byteHash64(bytes)
}

func (p *PrometheusHttp) readFiles(gid uint64, files *sync.Map, hashes *sync.Map, list []*PrometheusHttpMetric, init bool) (int, int) {

	entries := 0
	length := 0
	for _, v := range p.Files {

		cache := false
		obj, ok := files.Load(v.Name)
		if ok {
			if init {
				p.Log.Debugf("[%d] %s cache file: %s", gid, p.Name, v.Path)
			}
			cache = true
		}

		if _, err := os.Stat(v.Path); err == nil {

			if init {
				p.Log.Debugf("[%d] %s read file: %s", gid, p.Name, v.Path)
			}

			bytes, err := os.ReadFile(v.Path)
			if err != nil {
				p.Log.Error(err)
				continue
			}

			flag := true
			hash1 := byteHash64(bytes)

			if cache {
				hashv, ok := hashes.Load(v.Name)
				if ok {
					hash2, ok := hashv.(uint64)
					if ok && hash1 == hash2 {
						flag = false
						// claculate entries
						m, ok := obj.(map[string]interface{})
						if ok && p.fileInUse(v.Name, list) {
							entries = entries + len(m)
							for k := range m {
								if len(k) > length {
									length = len(k)
								}
							}
						}
					}
				}
			}

			if flag {

				hashes.Store(v.Name, hash1)

				tp := strings.Replace(filepath.Ext(v.Path), ".", "", 1)
				if v.Type != "" {
					tp = v.Type
				}

				var obj interface{}
				switch {
				case tp == "json":
					obj, err = p.readJson(bytes)
				case tp == "toml":
					obj, err = p.readToml(bytes)
				case (tp == "yaml") || (tp == "yml"):
					obj, err = p.readYaml(bytes)
				default:
					obj, err = p.readJson(bytes)
				}
				if err != nil {
					p.Log.Error(err)
					continue
				}
				// claculate entries
				m, ok := obj.(map[string]interface{})
				if ok && p.fileInUse(v.Name, list) {
					entries = entries + len(m)
					for k := range m {
						if len(k) > length {
							length = len(k)
						}
					}
				}
				files.Store(v.Name, obj)
			}
		}
	}

	return entries, length
}

// Gather is called by telegraf when the plugin is executed on its interval.
func (p *PrometheusHttp) Gather(acc telegraf.Accumulator) error {

	p.acc = acc

	var ds PrometheusHttpDatasource = nil
	gid := utils.GoRoutineID()
	p.readFiles(gid, &globalFiles, &globalHashes, p.Metrics, false)
	// Gather data
	err := p.gatherMetrics(gid, ds)
	return err
}

func (p *PrometheusHttp) Printf(format string, v ...interface{}) {
	p.Log.Debugf(format, v)
}

func (p *PrometheusHttp) Init() error {

	gid := utils.GoRoutineID()

	if p.Interval <= 0 {
		p.Interval = config.Duration(time.Second) * 5
	}

	if p.Name == "" {
		p.Name = "unknown"
	}
	if p.Timeout <= 0 {
		p.Timeout = config.Duration(time.Second) * 5
	}
	if p.Version == "" {
		p.Version = "v1"
	}
	if p.Step == "" {
		p.Step = "60"
	}
	if p.Prefix == "" {
		p.Prefix = pluginName
	}

	lMetrics := len(p.Metrics)
	if lMetrics == 0 {
		err := fmt.Errorf("[%d] %s no metrics found", gid, p.Name)
		p.Log.Error(err)
		return err
	}

	p.Log.Debugf("[%d] %s metrics amount: %d", gid, p.Name, lMetrics)

	for _, m := range p.Metrics {
		p.setDefaultMetric(gid, m)
	}

	//p.files = &sync.Map{}
	p.requests = NewRateCounter(time.Duration(p.Interval))
	p.errors = NewRateCounter(time.Duration(p.Interval))
	p.mtx = &sync.Mutex{}

	if len(p.Files) > 0 {

		entries, length := p.readFiles(gid, &globalFiles, &globalHashes, p.Metrics, true)

		seconds := time.Duration(p.Timeout).Seconds()

		if p.CacheDuration <= 0 {
			p.CacheDuration = config.Duration(time.Second * time.Duration(seconds))
		}

		config := bigcache.DefaultConfig(time.Duration(p.CacheDuration))
		config.Shards = 256
		config.CleanWindow = 0
		/*if seconds > 0 {
			t := int(math.Round(seconds / 2))
			if t > 1 {
				config.CleanWindow = time.Duration(time.Second * time.Duration(t))
			}
		}*/

		config.MaxEntriesInWindow = entries
		config.MaxEntrySize = length

		maxSizeInMb := 0
		if p.CacheSize > 0 {
			maxSizeInMb = int(p.CacheSize) / (1024 * 1024)
		} else {
			maxSizeInMb = (entries * length * int(seconds)) / (1024 * 1024)
		}
		if maxSizeInMb == 0 {
			maxSizeInMb = 1
		}
		config.HardMaxCacheSize = maxSizeInMb

		config.Logger = p
		config.Verbose = true

		cache, err := bigcache.NewBigCache(config)
		if err != nil {
			p.Log.Warnf("[%d] %s cache error: %s", gid, err)
		}
		p.cache = cache
	}

	return nil
}

func init() {
	inputs.Add(pluginName, func() telegraf.Input {
		return &PrometheusHttp{}
	})
}
