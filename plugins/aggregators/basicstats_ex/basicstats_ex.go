//go:generate ../../../tools/readme_config_includer/generator
package basicstats_ex

import (
	_ "embed"
	"fmt"
	"hash"
	"hash/fnv"
	"maps"
	"math"
	"time"

	"github.com/influxdata/telegraf"
	"github.com/influxdata/telegraf/plugins/aggregators"
)

//go:embed sample.conf
var sampleConfig string

const (
	count           = "count"
	min_            = "min"
	max_            = "max"
	mean            = "mean"
	s2              = "s2"
	stDev           = "stdev"
	sum             = "sum"
	diff            = "diff"
	nonNegativeDiff = "non_negative_diff"
	rate            = "rate"
	nonNegativeRate = "non_negative_rate"
	percentChange   = "percent_change"
	interval        = "interval"
)

type BasicStats struct {
	Stats          []string          `toml:"stats"`
	StatsSuffix    map[string]string `toml:"stats_suffix"`
	StatsSuffixAdd bool              `toml:"stats_suffix_add"`

	Aggregates map[string]map[string]string `toml:"aggregates"`

	GroupBy       []string `toml:"group_by"`
	groupByConfig map[string]interface{}

	Log telegraf.Logger

	cache       map[uint64]aggregate
	statsConfig *configuredStats
}

type configuredStats struct {
	count           bool
	min             bool
	max             bool
	mean            bool
	variance        bool
	stdev           bool
	sum             bool
	diff            bool
	nonNegativeDiff bool
	rate            bool
	nonNegativeRate bool
	percentChange   bool
	interval        bool
}

func NewBasicStats() *BasicStats {
	return &BasicStats{
		cache: make(map[uint64]aggregate),
	}
}

type aggregate struct {
	fields map[string]basicstats
	name   string
	tags   map[string]string
}

type basicstats struct {
	count    float64
	min      float64
	max      float64
	sum      float64
	mean     float64
	diff     float64
	rate     float64
	interval time.Duration
	M2       float64   //intermediate value for variance/stdev
	LAST     float64   //intermediate value for diff
	TIME     time.Time //intermediate value for rate
}

func (*BasicStats) SampleConfig() string {
	return sampleConfig
}

func generateHashID[T interface{}](tagsOrFields map[string]T, h hash.Hash64, b *BasicStats) map[string]string {
	foundGroupBy := map[string]string{}
	for name, value := range tagsOrFields {
		if _, ok := b.groupByConfig[name]; ok {
			convertedValue := fmt.Sprintf("%v", value)
			// save current value for each group by -> need for adding in tags
			foundGroupBy[name] = convertedValue

			h.Write([]byte(name))
			h.Write([]byte("\n"))
			h.Write([]byte(fmt.Sprintf("%v", convertedValue)))
			h.Write([]byte("\n"))
		}
	}
	return foundGroupBy
}

func (b *BasicStats) hashID(m telegraf.Metric) (uint64, map[string]string) {
	h := fnv.New64a()
	h.Write([]byte(m.Name()))
	h.Write([]byte("\n"))

	foundByTags := generateHashID(m.Tags(), h, b)
	foundFields := generateHashID(m.Fields(), h, b)

	foundByTagsAndFields := map[string]string{}
	maps.Copy(foundByTagsAndFields, foundByTags)
	maps.Copy(foundByTagsAndFields, foundFields)

	return h.Sum64(), foundByTagsAndFields
}

func (b *BasicStats) Add(in telegraf.Metric) {
	id, foundTagsAndFields := b.hashID(in)
	if len(foundTagsAndFields) == 0 {
		// skip - no tags or fields from group by
		return
	}
	if _, ok := b.cache[id]; !ok {
		// hit an uncached metric, create caches for first time:
		getAllTags := func(tags, fields map[string]string) map[string]string {
			maps.Copy(tags, fields)
			return tags
		}
		a := aggregate{
			name:   in.Name(),
			tags:   getAllTags(in.Tags(), foundTagsAndFields),
			fields: make(map[string]basicstats),
		}
		for _, field := range in.FieldList() {
			if fv, ok := convert(field.Value); ok {
				a.fields[field.Key] = basicstats{
					count: 1,
					min:   fv,
					max:   fv,
					mean:  fv,
					sum:   fv,
					diff:  0.0,
					rate:  0.0,
					M2:    0.0,
					LAST:  fv,
					TIME:  in.Time(),
				}
			}
		}
		b.cache[id] = a
	} else {
		for _, field := range in.FieldList() {
			if fv, ok := convert(field.Value); ok {
				if _, ok := b.cache[id].fields[field.Key]; !ok {
					// hit an uncached field of a cached metric
					b.cache[id].fields[field.Key] = basicstats{
						count:    1,
						min:      fv,
						max:      fv,
						mean:     fv,
						sum:      fv,
						diff:     0.0,
						rate:     0.0,
						interval: 0,
						M2:       0.0,
						LAST:     fv,
						TIME:     in.Time(),
					}
					continue
				}
				b.calculate(id, field, in, fv)
			}
		}
	}
}

func (b *BasicStats) calculate(id uint64, field *telegraf.Field, in telegraf.Metric, fv float64) {
	tmp := b.cache[id].fields[field.Key]
	//https://en.m.wikipedia.org/wiki/Algorithms_for_calculating_variance
	//variable initialization
	x := fv
	mean := tmp.mean
	m2 := tmp.M2
	//counter compute
	n := tmp.count + 1
	tmp.count = n
	//mean compute
	delta := x - mean
	mean = mean + delta/n
	tmp.mean = mean
	//variance/stdev compute
	m2 = m2 + delta*(x-mean)
	tmp.M2 = m2
	//max/min compute
	if fv < tmp.min {
		tmp.min = fv
	} else if fv > tmp.max {
		tmp.max = fv
	}
	//sum compute
	tmp.sum += fv
	//diff compute
	tmp.diff = fv - tmp.LAST
	//interval compute
	tmp.interval = in.Time().Sub(tmp.TIME)
	//rate compute
	if !in.Time().Equal(tmp.TIME) {
		tmp.rate = tmp.diff / tmp.interval.Seconds()
	}
	//store final data
	b.cache[id].fields[field.Key] = tmp
}

func (b *BasicStats) addSpecialAggregateTag(agg aggregate, aggTagName string) (map[string]interface{}, map[string]string) {
	fields := map[string]interface{}{}
	tags := map[string]string{}
	maps.Copy(tags, agg.tags)
	if _, ok := b.Aggregates[aggTagName]; ok {
		maps.Copy(tags, b.Aggregates[aggTagName])
	}
	return fields, tags
}

func (b *BasicStats) addFieldsToAcc(nameAggregate, nameMetric string, valueMetric any, acc telegraf.Accumulator, aggregate aggregate) {
	fields, tags := b.addSpecialAggregateTag(aggregate, nameAggregate)

	_, ok := b.Aggregates[nameAggregate]
	if !b.StatsSuffixAdd && ok { // if we will not add suffix, then aggregate metric must have aggregate tag
		fields[nameMetric] = valueMetric
	} else { // else if we set add suffix or not special aggregate tag metric create with suffix
		fields[nameMetric+"_"+b.StatsSuffix[nameAggregate]] = valueMetric
	}
	acc.AddFields(aggregate.name, fields, tags)
}

func (b *BasicStats) Push(acc telegraf.Accumulator) {
	for _, aggregate := range b.cache {
		for k, v := range aggregate.fields {
			if b.statsConfig.count {
				b.addFieldsToAcc(count, k, v.count, acc, aggregate)
			}
			if b.statsConfig.min {
				b.addFieldsToAcc(min_, k, v.min, acc, aggregate)
			}
			if b.statsConfig.max {
				b.addFieldsToAcc(max_, k, v.max, acc, aggregate)
			}
			if b.statsConfig.mean {
				b.addFieldsToAcc(mean, k, v.mean, acc, aggregate)
			}
			if b.statsConfig.sum {
				b.addFieldsToAcc(sum, k, v.sum, acc, aggregate)
			}

			//v.count always >=1
			if v.count > 1 {
				variance := v.M2 / (v.count - 1)

				if b.statsConfig.variance {
					b.addFieldsToAcc(s2, k, variance, acc, aggregate)
				}
				if b.statsConfig.stdev {
					b.addFieldsToAcc(stDev, k, math.Sqrt(variance), acc, aggregate)
				}
				if b.statsConfig.diff {
					b.addFieldsToAcc(diff, k, v.diff, acc, aggregate)
				}
				if b.statsConfig.nonNegativeDiff && v.diff >= 0 {
					b.addFieldsToAcc(nonNegativeDiff, k, v.diff, acc, aggregate)
				}
				if b.statsConfig.rate {
					b.addFieldsToAcc(rate, k, v.rate, acc, aggregate)
				}
				if b.statsConfig.percentChange {
					b.addFieldsToAcc(percentChange, k, v.diff/v.LAST*100, acc, aggregate)
				}
				if b.statsConfig.nonNegativeRate && v.diff >= 0 {
					b.addFieldsToAcc(nonNegativeRate, k, v.rate, acc, aggregate)
				}
				if b.statsConfig.interval {
					b.addFieldsToAcc(interval, k, v.interval.Nanoseconds(), acc, aggregate)
				}
			}
			//if count == 1 StdDev = infinite => so I won't send data
		}
	}
}

func (b *BasicStats) configureGroupBy() {
	b.groupByConfig = make(map[string]interface{}, len(b.GroupBy))
	for _, value := range b.GroupBy {
		b.groupByConfig[value] = ""
	}
}

func (b *BasicStats) updateStatsSuffix() {
	defaultSuffixes := map[string]string{
		count:           count,
		min_:            min_,
		max_:            max_,
		mean:            mean,
		s2:              s2,
		stDev:           stDev,
		sum:             sum,
		diff:            diff,
		nonNegativeDiff: nonNegativeDiff,
		rate:            rate,
		nonNegativeRate: nonNegativeRate,
		percentChange:   percentChange,
		interval:        interval,
	}
	if b.StatsSuffix == nil {
		b.StatsSuffix = make(map[string]string, len(defaultSuffixes))
	}
	for k, v := range defaultSuffixes {
		suffixNew, ok := b.StatsSuffix[k]
		if ok {
			b.StatsSuffix[k] = suffixNew
		} else {
			b.StatsSuffix[k] = v
		}
	}
}

// member function for logging.
func (b *BasicStats) parseStats() *configuredStats {
	parsed := &configuredStats{}

	for _, name := range b.Stats {
		switch name {
		case count:
			parsed.count = true
		case min_:
			parsed.min = true
		case max_:
			parsed.max = true
		case mean:
			parsed.mean = true
		case s2:
			parsed.variance = true
		case stDev:
			parsed.stdev = true
		case sum:
			parsed.sum = true
		case diff:
			parsed.diff = true
		case nonNegativeDiff:
			parsed.nonNegativeDiff = true
		case rate:
			parsed.rate = true
		case nonNegativeRate:
			parsed.nonNegativeRate = true
		case percentChange:
			parsed.percentChange = true
		case interval:
			parsed.interval = true
		default:
			b.Log.Warnf("Unrecognized basic stat %q, ignoring", name)
		}
	}

	return parsed
}

func (b *BasicStats) getConfiguredStats() {
	b.configureGroupBy()
	b.updateStatsSuffix()

	if b.Stats == nil {
		b.statsConfig = &configuredStats{
			count:           true,
			min:             true,
			max:             true,
			mean:            true,
			variance:        true,
			stdev:           true,
			sum:             false,
			nonNegativeDiff: false,
			rate:            false,
			nonNegativeRate: false,
			percentChange:   false,
		}
	} else {
		b.statsConfig = b.parseStats()
	}
}

func (b *BasicStats) Reset() {
	b.cache = make(map[uint64]aggregate)
}

func convert(in interface{}) (float64, bool) {
	switch v := in.(type) {
	case float64:
		return v, true
	case int64:
		return float64(v), true
	case uint64:
		return float64(v), true
	default:
		return 0, false
	}
}

func (b *BasicStats) Init() error {
	b.getConfiguredStats()

	return nil
}

func init() {
	aggregators.Add("basicstats_ex", func() telegraf.Aggregator {
		return NewBasicStats()
	})
}
