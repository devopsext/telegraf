package rancher_1_x

import (
	"errors"
	"fmt"
	"github.com/influxdata/telegraf"
	"github.com/influxdata/telegraf/plugins/inputs"
	rancher "github.com/rancher/go-rancher/v2"
	"log"
	"time"
)

// DockerCNTLogs object
type Rancher1X struct {
	Endpoint     	string `toml:"endpoint"`
	AccessKey    	string `toml:"access_key"`
	SecretKey    	string `toml:"secret_key"`
	APITimeoutSec	int `toml:"api_timeout_sec"`
	SvcEventTypesIn	[] string `toml:"service_events_types_include"`
//	SvcEventTypesEx	[] string `toml:"service_events_types_exclude"`
	rancherClient	*rancher.RancherClient

	envDict			map[string]map[string]string
	stackDict		map[string]map[string]interface{}
	serviceDict		map[string]map[string]interface{}
	svcEventsFilter *rancher.ListOpts
	previousRun		time.Time
	location		*time.Location
	acc           	telegraf.Accumulator
}

const sampleConfig = `
#[[inputs.rancher_1_x]]  
  
  ## Interval to gather data from API.
  ## the longer the interval the fewer request is made towards rancher API.
  # interval = "30s"
  
  ## Rancher API Endpoint
  #endpoint = "http://rancher.test.env"

  ## Rancher API Acess key (https://github.com/rancher/api-spec/blob/master/specification.md#authentication)
  # access_key = "*****"

  ## Rancher API Secret key (https://github.com/rancher/api-spec/blob/master/specification.md#authentication)
  # secret_key = "*****"

  ## Rancher API timeout in seconds. Default value - 5
  # api_timeout_sec = 5 

  ## Service event types to be included/excluded into/from statistics.
  ## 'like' syntax supported - "%.trigger%" 
  # service_events_types_include = ["val1","val2"]
  # service_events_types_exclude = ["val3","val4"]
`

//Service functions
func (r *Rancher1X) buildDataDict() (error) {
	var opts rancher.ListOpts
	var environments *rancher.ProjectCollection
	var err error

	var marker = 0
	var limit = 0

	opts = rancher.ListOpts{Filters: map[string]interface{}{"limit": limit, "all":"true"}}
	for true {

		environments,err = r.rancherClient.Project.List(&opts)
		if err !=nil {
			log.Printf("E! [inputs.rancher_1_x] Can't get environments list...")
			return err
		}

		for _, env := range(environments.Data) {
			r.envDict[env.Id] = map[string]string{"name":env.Name}
		}

		if environments.Collection.Pagination == nil || environments.Collection.Pagination.Partial == false {
			break
		}else{
			marker = marker + limit
			opts.Filters["marker"]= fmt.Sprintf("m%d",marker)
		}

	}



	marker = 0
	limit = 500
	opts = rancher.ListOpts{Filters: map[string]interface{}{"limit": limit,"all":"true"}}

	for true {
		stacks,err := r.rancherClient.Stack.List(&opts)
		if err !=nil {
			log.Printf("E! [inputs.rancher_1_x] Can't get stacks list...")
			return err
		}

		for _, stack := range(stacks.Data) {

			elem := make(map[string]interface{})
			elem["name"] = stack.Name
			elem["envId"] = stack.AccountId
			elem["serviceIds"] = stack.ServiceIds
			//r.stackDict[stack.Id] = map[string]interface{"name":stack.Name, "envId":stack.AccountId, "serviceIds":stack.ServiceIds}
			r.stackDict[stack.Id] = elem
		}

		if stacks.Collection.Pagination == nil || stacks.Collection.Pagination.Partial == false {
			break
		}else{
			marker = marker + limit
			opts.Filters["marker"]= fmt.Sprintf("m%d",marker)
		}

	}


	marker = 0
	limit = 500
	opts = rancher.ListOpts{Filters: map[string]interface{}{"limit": limit,"all":"true"}}

	for true {
		services, err := r.rancherClient.Service.List(&opts)
		if err !=nil{
			log.Printf("E! [inputs.rancher_1_x] Can't get service list...")
			return err
		}
		for _, service := range (services.Data){

			elem := make(map[string]interface{})
			elem["name"] = service.Name
			elem["stackId"] = service.StackId
			elem["state"] = service.State

			r.serviceDict[service.Id] = elem

		}

		if services.Collection.Pagination.Partial == false {
			break
		}else{
			marker = marker + limit
			opts.Filters["marker"]= fmt.Sprintf("m%d",marker)
		}

	}

	return nil
}

//Primary plugin interface
func (r *Rancher1X) Description() string {
	return "Scrap Rancher 1.x 'servicelog' API endpoint for completed events"
}

func (r *Rancher1X) SampleConfig() string { return sampleConfig }

func (r *Rancher1X) Gather(acc telegraf.Accumulator) error {

	var err error
	var field = make(map[string]interface{})
	var tags = map[string]string{}

	var events *rancher.ServiceLogCollection

	//Preparing timestamps for filter
	r.svcEventsFilter.Filters["endTime_gte"] = r.previousRun.Format(time.RFC3339) //Get all closed events since the previous run
	r.previousRun = time.Now().UTC() //update previous run timstamp

	events, err = r.rancherClient.ServiceLog.List(r.svcEventsFilter)
	if err !=nil {
		log.Printf("E! [inputs.rancher_1_x] Can't get the service log events...")
		return err
	}

	for _, event := range (events.Data) {

		if _, ok := r.serviceDict[event.ServiceId]; ok {

			if _, ok := r.envDict[event.AccountId]; ok {
				tags["environment"] = r.envDict[event.AccountId]["name"]
			}else{
				tags["environment"] = fmt.Sprintf("not found: %s",event.AccountId)
			}

			stackId := r.serviceDict[event.ServiceId]["stackId"].(string)
			if _, ok := r.stackDict[stackId]; ok{
				tags["stack"] = r.stackDict[stackId]["name"].(string)
			}else{
				tags["stack"] = fmt.Sprintf("not found: %s",stackId)
			}

			tags["service"] = r.serviceDict[event.ServiceId]["name"].(string)
			tags["description"] = event.Description
			tags["transactionId"] = event.TransactionId

			metricEndTime, err := time.Parse(time.RFC3339, event.EndTime)
			if err != nil {
				log.Printf("E! [inputs.rancher_1_x] Can't parse timestamp from '%s'...", event.EndTime)
				field["duration"] = 0.0
				metricEndTime = time.Now().UTC()
			} else {
				metricStarTime, err := time.Parse(time.RFC3339, event.Created)
				if err != nil {
					log.Printf("E! [inputs.rancher_1_x] Can't parse timestamp from '%s'...", event.Created)
					field["duration"] = 0.0
				} else { //All the dates parsed...
					//Calculate duration:
					field["duration"] = metricEndTime.Sub(metricStarTime).Seconds()
				}
			}

			acc.AddFields(fmt.Sprintf("service_events_%s",event.EventType), field, tags, metricEndTime)
		}else{
			acc.AddError(errors.New(fmt.Sprintf("Can't find service in data dict for event '%s'", event)))

		}


	}

	//fmt.Printf("%v",events)

	return nil
}

func (r *Rancher1X) Start(acc telegraf.Accumulator) error {
	var err error
	var apiTmeoutDuration time.Duration

	r.acc = acc

	if r.Endpoint == "" {
		return errors.New("'Endpoint' must be set!")
	}

	if r.APITimeoutSec == 0 {r.APITimeoutSec = 5}

	apiTmeoutDuration,err  = time.ParseDuration(fmt.Sprintf("%ds",r.APITimeoutSec))
	if err !=nil {
		log.Printf("D! [inputs.rancher_1_x] Can't parse timeout from 'api_timeout_sec'. Default value (5 sec.) will be used...")
		apiTmeoutDuration = time.Second * 5
	}

	r.rancherClient,err = rancher.NewRancherClient(&rancher.ClientOpts{
		Url:       r.Endpoint,
		AccessKey: r.AccessKey,
		SecretKey: r.SecretKey,
		//Timeout:   time.Second * time.Duration(r.APITimeoutSec),
		Timeout:   apiTmeoutDuration,
	})

	if err != nil {
		log.Printf("E! [inputs.rancher_1_x] Can't create rancher client...")
		return err
	}

	//Fill in dictionaries
	r.envDict = make(map[string]map[string]string)
	r.stackDict = make(map[string]map[string]interface{})
	r.serviceDict = make(map[string]map[string]interface{})
	err = r.buildDataDict()
	if err != nil {
		return err
	}

	//Preparing filter for gathering events
	r.svcEventsFilter = & rancher.ListOpts{Filters: map[string]interface{}{
		"subLog": "false"}}

	if len (r.SvcEventTypesIn) > 0 {
		r.svcEventsFilter.Filters["eventType_like"] = r.SvcEventTypesIn
	}


	r.previousRun = time.Now().UTC()
	r.location = r.previousRun.Location()

	return nil
}

func (r *Rancher1X) Stop() {
	log.Printf("D! [inputs.rancher_1_x] Stopped...")

}

func init() {
	inputs.Add("rancher_1_x", func() telegraf.Input { return &Rancher1X{} })

}
