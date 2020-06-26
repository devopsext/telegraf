package aliyuncms

import (
	"encoding/json"
	"github.com/aliyun/alibaba-cloud-sdk-go/sdk"
	"github.com/aliyun/alibaba-cloud-sdk-go/sdk/auth"
	"github.com/aliyun/alibaba-cloud-sdk-go/sdk/requests"
	"github.com/aliyun/alibaba-cloud-sdk-go/sdk/responses"
	"github.com/aliyun/alibaba-cloud-sdk-go/services/ecs"
	"github.com/aliyun/alibaba-cloud-sdk-go/services/rds"
	"github.com/aliyun/alibaba-cloud-sdk-go/services/slb"
	"github.com/aliyun/alibaba-cloud-sdk-go/services/vpc"
	"github.com/influxdata/telegraf/internal/limiter"
	"github.com/pkg/errors"
	"reflect"
	"regexp"
	"strconv"
	"sync"
	"time"
)

// https://www.alibabacloud.com/help/doc-detail/40654.htm?gclid=Cj0KCQjw4dr0BRCxARIsAKUNjWTAMfyVUn_Y3OevFBV3CMaazrhq0URHsgE7c0m0SeMQRKlhlsJGgIEaAviyEALw_wcB
var aliyunRegionList = []string{
	"cn-qingdao",
	"cn-beijing",
	"cn-zhangjiakou",
	"cn-huhehaote",
	"cn-hangzhou",
	"cn-shanghai",
	"cn-shenzhen",
	"cn-heyuan",
	"cn-chengdu",
	"cn-hongkong",
	"ap-southeast-1",
	"ap-southeast-2",
	"ap-southeast-3",
	"ap-southeast-5",
	"ap-south-1",
	"ap-northeast-1",
	"us-west-1",
	"us-east-1",
	"eu-central-1",
	"eu-west-1",
	"me-east-1",
}

type dscRequest interface {
}

type discoveryTool struct {
	disReq             map[string]dscRequest  //Discovery request (specific per object type)
	rateLimit          int                    //Rate limit for API query, as it is limited by API backend
	reqDefaultPageSize int                    //Default page size while quering data from API (how many objects per request)
	cli                map[string]*sdk.Client //API client, which perform discovery request

	responseRootKey     string //Root key in JSON response where to look for discovery data
	responseObjectIdKey string //Key in element of array under root key, that stores object ID
	//for ,majority of cases it would be InstanceId, for OSS it is BucketName. This key is also used in dimension filtering// )
	wg                sync.WaitGroup              //WG for primary discovery goroutine
	discoveryInterval time.Duration               //Discovery interval
	done              chan bool                   //Done channel to stop primary discovery goroutine
	discDataChan      chan map[string]interface{} //Discovery data
}

type DescribeLOSSRequest struct {
	*requests.RpcRequest
}

func NewDiscoveryTool(regions []string, project string, credential auth.Credential, rateLimit int, discoveryInterval time.Duration) (*discoveryTool, error) {
	var (
		disReq              = map[string]dscRequest{}
		cli                 = map[string]*sdk.Client{}
		parseRootKey        = regexp.MustCompile(`Describe(.*)`)
		reponseRootKey      string
		responseObjectIdKey string
		err                 error
	)

	if len(regions) == 0 {
		regions = aliyunRegionList
		lg.logW("Discovery regions are not provided! Data will be queried across %d regions!", len(aliyunRegionList))
	}

	if rateLimit == 0 { //Can be a rounding case
		rateLimit = 1
	}

	for _, region := range regions {
		switch project {
		case "acs_ecs_dashboard":
			disReq[region] = ecs.CreateDescribeInstancesRequest()
			responseObjectIdKey = "InstanceId"
		case "acs_rds_dashboard":
			disReq[region] = rds.CreateDescribeDBInstancesRequest()
			responseObjectIdKey = "DBInstanceId"
		case "acs_slb_dashboard":
			disReq[region] = slb.CreateDescribeLoadBalancersRequest()
			responseObjectIdKey = "LoadBalancerId"
		case "acs_memcache":
			return nil, errors.Errorf("no discovery support for project %q", project)
		case "acs_ocs":
			return nil, errors.Errorf("no discovery support for project %q", project)
		case "acs_oss":
			//oss is really complicated
			//it is on it's own format
			return nil, errors.Errorf("no discovery support for project %q", project)

			//As a possible solution we can
			//mimic to request format supported by oss

			//req := DescribeLOSSRequest{
			//	RpcRequest: &requests.RpcRequest{},
			//}
			//req.InitWithApiInfo("oss", "2014-08-15", "DescribeDBInstances", "oss", "openAPI")
		case "acs_vpc_eip":
			disReq[region] = vpc.CreateDescribeEipAddressesRequest()
			responseObjectIdKey = "AllocationId"
		case "acs_kvstore":
			return nil, errors.Errorf("no discovery support for project %q", project)
		case "acs_mns_new":
			return nil, errors.Errorf("no discovery support for project %q", project)
		case "acs_cdn":
			//API replies are in its own format.
			return nil, errors.Errorf("no discovery support for project %q", project)
		case "acs_polardb":
			return nil, errors.Errorf("no discovery support for project %q", project)
		case "acs_gdb":
			return nil, errors.Errorf("no discovery support for project %q", project)
		case "acs_ads":
			return nil, errors.Errorf("no discovery support for project %q", project)
		case "acs_mongodb":
			return nil, errors.Errorf("no discovery support for project %q", project)
		case "acs_express_connect":
			return nil, errors.Errorf("no discovery support for project %q", project)
		case "acs_fc":
			return nil, errors.Errorf("no discovery support for project %q", project)
		case "acs_nat_gateway":
			return nil, errors.Errorf("no discovery support for project %q", project)
		case "acs_sls_dashboard":
			return nil, errors.Errorf("no discovery support for project %q", project)
		case "acs_containerservice_dashboard":
			return nil, errors.Errorf("no discovery support for project %q", project)
		case "acs_vpn":
			return nil, errors.Errorf("no discovery support for project %q", project)
		case "acs_bandwidth_package":
			return nil, errors.Errorf("no discovery support for project %q", project)
		case "acs_cen":
			return nil, errors.Errorf("no discovery support for project %q", project)
		case "acs_ens":
			return nil, errors.Errorf("no discovery support for project %q", project)
		case "acs_opensearch":
			return nil, errors.Errorf("no discovery support for project %q", project)
		case "acs_scdn":
			return nil, errors.Errorf("no discovery support for project %q", project)
		case "acs_drds":
			return nil, errors.Errorf("no discovery support for project %q", project)
		case "acs_iot":
			return nil, errors.Errorf("no discovery support for project %q", project)
		case "acs_directmail":
			return nil, errors.Errorf("no discovery support for project %q", project)
		case "acs_elasticsearch":
			return nil, errors.Errorf("no discovery support for project %q", project)
		case "acs_ess_dashboard":
			return nil, errors.Errorf("no discovery support for project %q", project)
		case "acs_streamcompute":
			return nil, errors.Errorf("no discovery support for project %q", project)
		case "acs_global_acceleration":
			return nil, errors.Errorf("no discovery support for project %q", project)
		case "acs_hitsdb":
			return nil, errors.Errorf("no discovery support for project %q", project)
		case "acs_kafka":
			return nil, errors.Errorf("no discovery support for project %q", project)
		case "acs_openad":
			return nil, errors.Errorf("no discovery support for project %q", project)
		case "acs_pcdn":
			return nil, errors.Errorf("no discovery support for project %q", project)
		case "acs_dcdn":
			return nil, errors.Errorf("no discovery support for project %q", project)
		case "acs_petadata":
			return nil, errors.Errorf("no discovery support for project %q", project)
		case "acs_videolive":
			return nil, errors.Errorf("no discovery support for project %q", project)
		case "acs_hybriddb":
			return nil, errors.Errorf("no discovery support for project %q", project)
		case "acs_adb":
			return nil, errors.Errorf("no discovery support for project %q", project)
		case "acs_mps":
			return nil, errors.Errorf("no discovery support for project %q", project)
		case "acs_maxcompute_prepay":
			return nil, errors.Errorf("no discovery support for project %q", project)
		case "acs_hdfs":
			return nil, errors.Errorf("no discovery support for project %q", project)
		case "acs_ddh":
			return nil, errors.Errorf("no discovery support for project %q", project)
		case "acs_hbr":
			return nil, errors.Errorf("no discovery support for project %q", project)
		case "acs_hdr":
			return nil, errors.Errorf("no discovery support for project %q", project)
		case "acs_cds":
			return nil, errors.Errorf("no discovery support for project %q", project)
		default:
			return nil, errors.Errorf("project %q is not recognized by discovery...", project)
		}

		cli[region], err = sdk.NewClientWithOptions(region, sdk.NewConfig(), credential)
		if err != nil {
			return nil, err
		}
	}

	if len(disReq) == 0 || len(cli) == 0 {
		return nil, errors.Errorf("Can't build discovery requests for project: %q,\nregions: %v", project, regions)
	}

	//Getting response root key (if not set already). This is to be able to parse discovery responses
	//As they differ per object type
	if reponseRootKey == "" {
		//Discovery requests are of the same type per every region, so pick the first one
		rpcReq, err := getRpcReqFromDiscoveryRequest(disReq[regions[0]])
		//This means that the discovery request is not of proper type/kind
		if err != nil {
			return nil, errors.Errorf("Can't parse rpc request object from  discovery request %v", disReq[regions[0]])
		}
		//The action name is of the following format Describe<Project related title for managed instances>,
		//For example: DescribeLoadBalancers -> for SLB project, or DescribeInstances for ECS project
		//We will use it to construct root key name in the discovery API response.
		//It follows the following logic: for 'DescribeLoadBalancers' action in discovery request we get the response
		//in json of the following structure:
		//{
		//  ...
		//  "LoadBalancers": {
		//   "LoadBalancer": [ here comes objects, one per every instance]
		//}
		//}
		//As we can see, the root key is a part of action name, except first word (part) 'Describe'
		result := parseRootKey.FindStringSubmatch(rpcReq.GetActionName())
		if result == nil || len(result) != 2 {
			return nil, errors.Errorf("Can't parse the discovery response root key from request action name %q", rpcReq.GetActionName())
		}
		reponseRootKey = result[1]
	}

	return &discoveryTool{
		disReq:              disReq,
		cli:                 cli,
		responseRootKey:     reponseRootKey,
		responseObjectIdKey: responseObjectIdKey,
		rateLimit:           rateLimit,
		discoveryInterval:   discoveryInterval,
		reqDefaultPageSize:  20,
		discDataChan:        make(chan map[string]interface{}, 1),
	}, nil
}

func (dt *discoveryTool) getDiscoveredDataFromResponse(resp *responses.CommonResponse) (discData []interface{}, totalCount int, pageSize int, pageNumber int, err error) {
	var (
		fullOutput    = map[string]interface{}{}
		data          []byte
		foundDataItem bool
		foundRootKey  bool
	)

	data = resp.GetHttpContentBytes()
	if data == nil { //No data
		return nil, 0, 0, 0, errors.Errorf("No data in response to be parsed")
	}

	err = json.Unmarshal(data, &fullOutput)
	if err != nil {
		return nil, 0, 0, 0, errors.Errorf("Can't parse JSON from discovery response: %v", err)
	}

	for key, val := range fullOutput {
		switch key {
		case dt.responseRootKey:
			foundRootKey = true
			rootKeyVal, ok := val.(map[string]interface{})
			if !ok {
				return nil, 0, 0, 0, errors.Errorf("Content of root key %q, is not an object: %v", key, val)
			}

			//It should contain the array with discovered data
			for _, item := range rootKeyVal {

				if discData, foundDataItem = item.([]interface{}); foundDataItem {
					break
				}
			}
			if !foundDataItem {
				return nil, 0, 0, 0, errors.Errorf("Didn't find array item in root key %q", key)
			}
		case "TotalCount":
			totalCount = int(val.(float64))
		case "PageSize":
			pageSize = int(val.(float64))
		case "PageNumber":
			pageNumber = int(val.(float64))
		}

	}
	if !foundRootKey {
		return nil, 0, 0, 0, errors.Errorf("Didn't find root key %q in discovery response", dt.responseRootKey)
	}

	return
}

func getRpcReqFromDiscoveryRequest(req dscRequest) (*requests.RpcRequest, error) {

	if reflect.ValueOf(req).Type().Kind() != reflect.Ptr ||
		reflect.ValueOf(req).IsNil() {
		return nil, errors.Errorf("Not expected type of the discovery request object: %q, %q", reflect.ValueOf(req).Type(), reflect.ValueOf(req).Kind())
	}

	ptrV := reflect.Indirect(reflect.ValueOf(req))

	for i := 0; i < ptrV.NumField(); i++ {

		if ptrV.Field(i).Type().String() == "*requests.RpcRequest" {
			if !ptrV.Field(i).CanInterface() {
				return nil, errors.Errorf("Can't get interface of %v", ptrV.Field(i))
			}

			rpcReq, ok := ptrV.Field(i).Interface().(*requests.RpcRequest)

			if !ok {
				return nil, errors.Errorf("Cant convert interface of %v to '*requests.RpcRequest' type", ptrV.Field(i).Interface())
			}

			return rpcReq, nil
		}
	}
	return nil, errors.Errorf("Didn't find *requests.RpcRequest embedded struct in %q", ptrV.Type())
}

func (dt *discoveryTool) getDiscoveryData(cli *sdk.Client, req *requests.CommonRequest, limiter chan bool) (map[string]interface{}, error) {
	var (
		err           error
		resp          *responses.CommonResponse
		data          []interface{}
		discoveryData []interface{}
		totalCount    int
		pageNumber    int
	)
	defer delete(req.QueryParams, "PageNumber")

	for {
		if limiter != nil {
			<-limiter //Rate limiting
		}

		resp, err = cli.ProcessCommonRequest(req)
		if err != nil {
			return nil, err
		}

		data, totalCount, _, pageNumber, err = dt.getDiscoveredDataFromResponse(resp)
		if err != nil {
			return nil, err
			continue
		}
		discoveryData = append(discoveryData, data...)

		//Pagination
		pageNumber++
		req.QueryParams["PageNumber"] = strconv.Itoa(pageNumber)

		if len(discoveryData) == totalCount { //All data received
			preparedData, err := dt.restructureData(discoveryData)
			if err != nil {
				return nil, err
			}
			return preparedData, nil
		}

	}

}

func (dt *discoveryTool) restructureData(data []interface{}) (map[string]interface{}, error) {
	returnData := map[string]interface{}{}

	for _, raw := range data {
		if elem, ok := raw.(map[string]interface{}); ok {
			if objectId, ok := elem[dt.responseObjectIdKey].(string); ok {
				returnData[objectId] = elem
			}
		} else {
			return nil, errors.Errorf("Can't parse input data element, not a map[string]interface{} type")
		}

	}
	return returnData, nil
}

func (dt *discoveryTool) getDiscoveryDataAllRegions(limiter chan bool) (map[string]interface{}, error) {
	var (
		err        error
		req        *requests.CommonRequest
		data       map[string]interface{}
		resultData = map[string]interface{}{}
	)

	for region, cli := range dt.cli {
		req, err = dt.getCommonDiscoveryRequest(region)
		if err != nil {
			return nil, err
		}

		data, err = dt.getDiscoveryData(cli, req, limiter)
		if err != nil {
			return nil, err
		}

		for k, v := range data {
			resultData[k] = v
		}

	}
	return resultData, nil

}

func (dt *discoveryTool) getCommonDiscoveryRequest(region string) (*requests.CommonRequest, error) {

	req, ok := dt.disReq[region]
	if !ok {
		return nil, errors.Errorf("Error building common discovery request: not valid region %q", region)
	}

	rpcReq, err := getRpcReqFromDiscoveryRequest(req)
	if err != nil {
		return nil, err
	}

	commonRequest := requests.NewCommonRequest()
	commonRequest.Method = rpcReq.GetMethod()
	commonRequest.Product = rpcReq.GetProduct()
	commonRequest.Domain = rpcReq.GetDomain()
	commonRequest.Version = rpcReq.GetVersion()
	commonRequest.Scheme = rpcReq.GetScheme()
	commonRequest.ApiName = rpcReq.GetActionName()
	commonRequest.QueryParams = rpcReq.QueryParams
	commonRequest.QueryParams["PageSize"] = strconv.Itoa(dt.reqDefaultPageSize)
	commonRequest.TransToAcsRequest()

	return commonRequest, nil
}

func (dt *discoveryTool) Start() {
	var (
		err      error
		data     map[string]interface{}
		lastData map[string]interface{}
	)

	//Initializing channel
	dt.done = make(chan bool)

	dt.wg.Add(1)
	go func() {
		defer dt.wg.Done()

		ticker := time.NewTicker(dt.discoveryInterval)
		defer ticker.Stop()

		lmtr := limiter.NewRateLimiter(dt.rateLimit, time.Second)
		defer lmtr.Stop()

		for {
			select {
			case <-dt.done:
				return
			case <-ticker.C:

				data, err = dt.getDiscoveryDataAllRegions(lmtr.C)
				if err != nil {
					lg.logE("Can't get discovery data: %v", err)
					continue
				}

				if !reflect.DeepEqual(data, lastData) {
					lastData = nil
					lastData = map[string]interface{}{}
					for k, v := range data {
						lastData[k] = v
					}

					//send discovery data in blocking mode
					dt.discDataChan <- data
				}

			}
		}
	}()
}

func (dt *discoveryTool) Stop() {

	close(dt.done)

	//Shutdown timer
	timer := time.NewTimer(time.Second * 3)
	defer timer.Stop()
L:
	for { //Unblock go routine
		select {
		case <-timer.C:
			break L
		case <-dt.discDataChan:
		}
	}

	dt.wg.Wait()
}
