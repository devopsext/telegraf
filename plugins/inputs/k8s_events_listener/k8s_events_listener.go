package k8s_events_listener

import (
	"bytes"
	"flag"
	"fmt"
	"sync"

	"github.com/influxdata/telegraf"
	"github.com/influxdata/telegraf/plugins/inputs"
	"github.com/nsf/jsondiff"
	"github.com/pkg/errors"

	"log"
	"os"
	"path/filepath"
	"time"

	jSerializer "k8s.io/apimachinery/pkg/runtime/serializer/json"
	k8sScheme "k8s.io/client-go/kubernetes/scheme"
	k8sRest "k8s.io/client-go/rest"
	k8sClientCmd "k8s.io/client-go/tools/clientcmd"
	k8s "k8s.io/client-go/kubernetes"
	k8sWatch "k8s.io/apimachinery/pkg/watch"

	apiAppsV1 "k8s.io/api/apps/v1"
	apiCoreV1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

)

// DockerCNTLogs object
type k8sEventsListener struct {
	acc            telegraf.Accumulator
	Mode           string `toml:"mode"` //In-cluster or Out-cluster
	KubeConfigFile string `toml:"kubectl_config_file"`
	Namespace	   string `toml:"namespace"` //empty - all namespaces, or set to concrete.
	TargetObjects  []string `toml:"target_objects"` //Only deployment supported
	config         * k8sRest.Config
	clientSet      * k8s.Clientset
	serializer	   * jSerializer.Serializer
	watchers	   map[string]k8sWatch.Interface
	eventsTable	   map[string]map[string]map[string]interface{}
	lockers		   map[string]*sync.RWMutex
}

const inputName = "k8s_events_listener"
const sampleConfig = `
#[[inputs.k8s_events_listener]]  
  
  ## Interval to gather data from API.
  ## the longer the interval the fewer request is made towards rancher API.
  # interval = "60s"
  
  ## Rancher API Endpoint
  #endpoint = "http://rancher.test.env"

  ## Rancher API Acess key (https://github.com/rancher/api-spec/blob/master/specification.md#authentication)
  # access_key = "*****"

  ## Rancher API Secret key (https://github.com/rancher/api-spec/blob/master/specification.md#authentication)
  # secret_key = "*****"

  ## Rancher API timeout in seconds. Default value - 5
  # api_timeout_sec = 5 

  ## Initial offset - for the first collection.
  ## Standard syntax supported (should be equal to interval)
  # offset = "60s"

  ## Service event types to be included into statistics.
  ## Only 1 item in list is supported (inspite that there is an array)
  ## 'like' syntax supported - "%.trigger%"
  # service_events_types_include = ["val1","val2"]  
`

const (
	deploymentStateProgressing = "Progressing"
	deploymentStateStable = "Stable"
)
//Primary plugin interface
func (k8sEL *k8sEventsListener) Description() string {
	return "Listener for events of various k8s objects"
}

func (k8sEL *k8sEventsListener) SampleConfig() string { return sampleConfig }

func (k8sEL *k8sEventsListener) Gather(acc telegraf.Accumulator) error {
	var zeroLock = &sync.RWMutex{}
	var field = make(map[string]interface{})
	var tags = map[string]string{}

	defer func(){
		for _,lock := range k8sEL.lockers {
			if *lock != *zeroLock{
				lock.Unlock()}
		}
	}()
	//Here we will gather info about objects in progress
	for eventsSource,table := range k8sEL.eventsTable {
		k8sEL.lockers[eventsSource].Lock()
		for uid,entry := range table{

			//Here we can send the event
			if entry["state"] == deploymentStateProgressing {
				tags["name"] = entry["name"].(string)
				tags["namespace"] = entry["namespace"].(string)
				tags["uid"] = uid

				tags["location"] = entry["location"].(string)
				tags["transaction"]	= entry["state.Code"].(string)
				tags["message"] = entry["state.Message"].(string)
				tags["transaction_start_ts"] = entry["state.ts"].(time.Time).Format(time.RFC3339)

				field["duration"] = time.Now().UTC().Sub(entry["state.ts"].(time.Time)).Seconds()

				k8sEL.acc.AddFields(fmt.Sprintf("inprogress_%s_events",eventsSource), field, tags, time.Now().UTC())
			}

		}
		k8sEL.lockers[eventsSource].Unlock()

	}

	return nil
}


func getObjectStringRep( obj interface{}) string {
	var name,namespace,revision string
	switch object := obj.(type) {
	case *apiCoreV1.Pod:
		{
			name = object.Name
			namespace = object.Namespace
			revision = object.ResourceVersion
		}
	case *apiAppsV1.ReplicaSet:
		{
			name = object.Name
			namespace = object.Namespace
			revision = object.ResourceVersion
		}
	case *apiAppsV1.StatefulSet:
		{
			name = object.Name
			namespace = object.Namespace
			revision = object.ResourceVersion
		}
	case *apiAppsV1.DaemonSet:
		{
			name = object.Name
			namespace = object.Namespace
			revision = object.ResourceVersion
		}
	case *apiAppsV1.Deployment:
		{
			name = object.Name
			namespace = object.Namespace
			revision = object.ResourceVersion
		}
	default:
		fmt.Printf("Warn! Unknown object %v\n",obj)
		return ""
	}
	return fmt.Sprintf("%T,%s.%s rev: %s",obj,name,namespace,revision)
}

func getDeploymentState(deployment *apiAppsV1.Deployment) string {
	if deployment.Generation == deployment.Status.ObservedGeneration &&
		*deployment.Spec.Replicas == deployment.Status.Replicas &&
		*deployment.Spec.Replicas == deployment.Status.AvailableReplicas &&
		*deployment.Spec.Replicas == deployment.Status.ReadyReplicas &&
		*deployment.Spec.Replicas == deployment.Status.UpdatedReplicas &&
		deployment.Status.UnavailableReplicas == 0 {
		return deploymentStateStable
	}else {
		return deploymentStateProgressing
	}

}

func getDeploymentStatusConditionByType (conditions []apiAppsV1.DeploymentCondition,condType string) (*apiAppsV1.DeploymentCondition, error) {
	for _,item := range conditions {
		if string(item.Type) == condType {
			return &item,nil
		}
	}
	return nil,errors.New(fmt.Sprintf("Can't find condition with type '%s'",condType))
}

func getDeploymentStateTs(deployment *apiAppsV1.Deployment, state string) (time.Time,error) {
	var condProcessing *apiAppsV1.DeploymentCondition
	var condAvailable *apiAppsV1.DeploymentCondition
	var err,conProcessingErr,condAvailableErr error



	switch state {
	case deploymentStateProgressing:
		condProcessing,err = getDeploymentStatusConditionByType(deployment.Status.Conditions,"Progressing")
		if err != nil{return time.Time{},err}
		return condProcessing.LastUpdateTime.Time,nil
	case deploymentStateStable:
		condProcessing,conProcessingErr = getDeploymentStatusConditionByType(deployment.Status.Conditions,"Progressing")
		condAvailable,condAvailableErr = getDeploymentStatusConditionByType(deployment.Status.Conditions,"Available")

		if conProcessingErr!= nil {
			if condAvailableErr != nil{return time.Time{},condAvailableErr}
			return condAvailable.LastUpdateTime.Time, nil
		}

		//Both available, Returning maximum of 2
		if condAvailable.LastUpdateTime.Time.After(condProcessing.LastUpdateTime.Time) {
			return condAvailable.LastUpdateTime.Time, nil
		}else {
			return condProcessing.LastUpdateTime.Time, nil
		}
	default:
		return time.Time{},errors.New(fmt.Sprintf("Unknown state: '%s'",state))
	}
}

func isDeploymentEventNoisy(deployment *apiAppsV1.Deployment, currentState string, prevEventEntry map[string]interface{}) bool {

	if ((deployment.Generation == 1 && len(deployment.Status.Conditions) !=0 ) || deployment.Generation != 1)&& // generation = 1 means creation of new deployment if status is empty it is the first event in creation of new depl.
		deployment.Generation == prevEventEntry["generation"] && // if generation changed since previous event, this means that upgrade started
		currentState == deploymentStateProgressing &&
		prevEventEntry["state"] == currentState  {
		return true
	}

	return false

}



func (k8sEL *k8sEventsListener) deploymentEventsListener(watch k8sWatch.Interface,deplEvents map[string]map[string]interface{},lock *sync.RWMutex) {

	var objSpecDiff = ""
	var dif jsondiff.Difference
	var currentObjSpecJson  = new (bytes.Buffer)
	var currentObjSpecTxt  []byte
	var jsonDiffOptions = jsondiff.DefaultConsoleOptions()

	var field = make(map[string]interface{})
	var tags = map[string]string{}

	var zeroLock = &sync.RWMutex{}


	defer func() {
		if *lock != *zeroLock{
			lock.Unlock()}
		}()

	for event := range watch.ResultChan() {
			eventReceived := time.Now().UTC()

		deployment := event.Object.(*apiAppsV1.Deployment)

		currentObjSpecJson.Reset()
		k8sEL.serializer.Encode(deployment,currentObjSpecJson)
		currentObjSpecTxt = currentObjSpecJson.Bytes()
		//
		lock.Lock()
		//
		if _, ok := deplEvents[string(deployment.UID)]; !ok {
			//Init event, we just started and we receievd event that actually describes the current state of the object

			if event.Type == k8sWatch.Deleted {
				log.Printf("W! [inputs.%s] WARN! Receievd '%s' event on non existing event item entry...\n",
					inputName,
					k8sWatch.Deleted)
				lock.Unlock()
				continue
				}

			deplEvents[string(deployment.UID)] = map[string]interface{}{}

			//General INFO
			//deplEvents[string(deployment.UID)]["self"] = deployment //BE AWARE! the object is mutating
			deplEvents[string(deployment.UID)]["name"] = deployment.Name
			deplEvents[string(deployment.UID)]["namespace"] = deployment.Namespace
			deplEvents[string(deployment.UID)]["location"] = fmt.Sprintf("%s.%s",deployment.Namespace,deployment.Name)

			deplEvents[string(deployment.UID)]["creationTs"] = deployment.CreationTimestamp

			deplEvents[string(deployment.UID)]["generation"] = deployment.Generation
			deplEvents[string(deployment.UID)]["spec"] = []byte{}
			deplEvents[string(deployment.UID)]["spec"] = append(currentObjSpecTxt[:0:0], currentObjSpecTxt...)

			//deplEvents[string(deployment.UID)]["resourceVersion"] = deployment.ResourceVersion
			//deplEvents[string(deployment.UID)]["targetReplicas"] = deployment.Spec.Replicas
			//deplEvents[string(deployment.UID)]["status.observedGeneration"] = deployment.Status.ObservedGeneration
			//deplEvents[string(deployment.UID)]["status.replicas"] = deployment.Status.Replicas
			//deplEvents[string(deployment.UID)]["status.availableReplicas"] = deployment.Status.AvailableReplicas
			//deplEvents[string(deployment.UID)]["status.readyReplicas"] = deployment.Status.ReadyReplicas
			//deplEvents[string(deployment.UID)]["status.updatedReplicas"] = deployment.Status.UpdatedReplicas
			//deplEvents[string(deployment.UID)]["status.unavailableReplicas"] = deployment.Status.UnavailableReplicas
			//

			//Specific flags and stuff to deal with events processing
			deplEvents[string(deployment.UID)]["readyToPush"] = false //Not be pushed to output!!!
			deplEvents[string(deployment.UID)]["state.Message"] = ""

			if deployment.Generation == 1 && len(deployment.Status.Conditions) == 0 { //new deployment
				deplEvents[string(deployment.UID)]["state"] = deploymentStateProgressing
				deplEvents[string(deployment.UID)]["state.Code"] = "NewDeployment"
				deplEvents[string(deployment.UID)]["state.ts"] = eventReceived
				log.Printf("W! [inputs.%s] Catched new deployment: '%s'\n",inputName, deplEvents[string(deployment.UID)]["location"])
			}else{ //stable or progressing deployment that already exist

				deplEvents[string(deployment.UID)]["state"] = getDeploymentState(deployment)
				//Unknown, as it can be progressing of previously triggered deployment (that is either in error or normal)
				//Also can be a deployment that is stabke state (nothing is hapenning)
				deplEvents[string(deployment.UID)]["state.Code"] = ""
				ts,err := getDeploymentStateTs(deployment,deplEvents[string(deployment.UID)]["state"].(string))
				if err!=nil {
					log.Printf("E! [inputs.%s] Can't detect TS for event state '%s'. Reason: %v\n",
						inputName,
						deplEvents[string(deployment.UID)]["state"],
						err)
				}
				deplEvents[string(deployment.UID)]["state.ts"] = ts.UTC()

			}

		}else{
			//Not a new object... something is happened:
			// 	IN general there are few cases:
			//	1.Deletion of deployment (special case)
			//	2.Noise events that describe intermediate steps in creation/upgrade process
			//	3.Upgrade of deployment started (new deployments are handled in previous clause)
			//	4.Upgrade of deployment/Creating of new deployment ended (succesfully)

			currentState := getDeploymentState(deployment)

			if event.Type == k8sWatch.Deleted {
				// 1.Deletion of deployment (special case)
				log.Printf("D! [inputs.%s] Deleting entry from events table...\n",inputName)
				delete (deplEvents, string(deployment.UID))
				//continue

			}else if isDeploymentEventNoisy(deployment,currentState,deplEvents[string(deployment.UID)]){

				// 2.Noisy events that describe intermediate steps in creation/upgrade process
				log.Printf("W! [inputs.%s] Noisy event, filtered: evt: %v, %s %s \n",
					inputName,event.Type,time.Now().Format(time.RFC3339),getObjectStringRep(deployment))

				//This is to put additional info about current situation
				statusCond,err := getDeploymentStatusConditionByType(deployment.Status.Conditions,"Progressing")
				if err == nil {
					if string(statusCond.Status) == "False" {
						deplEvents[string(deployment.UID)]["state.Message"] = fmt.Sprintf("%s:%s",statusCond.Reason,statusCond.Message)
					}
				}

				//continue

			}else{
				// 3&4

				//Update info
				deplEvents[string(deployment.UID)]["generation"] = deployment.Generation

				//deplEvents[string(deployment.UID)]["resourceVersion"] = deployment.ResourceVersion
				//deplEvents[string(deployment.UID)]["targetReplicas"] = deployment.Spec.Replicas
				//deplEvents[string(deployment.UID)]["status.observedGeneration"] = deployment.Status.ObservedGeneration
				//deplEvents[string(deployment.UID)]["status.replicas"] = deployment.Status.Replicas
				//deplEvents[string(deployment.UID)]["status.availableReplicas"] = deployment.Status.AvailableReplicas
				//deplEvents[string(deployment.UID)]["status.readyReplicas"] = deployment.Status.ReadyReplicas
				//deplEvents[string(deployment.UID)]["status.updatedReplicas"] = deployment.Status.UpdatedReplicas
				//deplEvents[string(deployment.UID)]["status.unavailableReplicas"] = deployment.Status.UnavailableReplicas

				deplEvents[string(deployment.UID)]["state"] = currentState
				//

				if currentState == deploymentStateProgressing {
					//3.Upgrade of deployment started (new deployments are handled in previous clause)
					//

					deplEvents[string(deployment.UID)]["state.ts"] = time.Now().UTC()
					deplEvents[string(deployment.UID)]["state.Code"] = "UpgradeStarted"
					deplEvents[string(deployment.UID)]["state.Finalized.Code"] = ""
					deplEvents[string(deployment.UID)]["state.Finalized.ts"] = ""
					deplEvents[string(deployment.UID)]["state.Message"] = ""
					deplEvents[string(deployment.UID)]["readyToPush"] = false

					log.Printf("W! [inputs.%s] Catched start of upgrade of '%s'\n",inputName, deplEvents[string(deployment.UID)]["location"])

				}else{
					// 4.Upgrade of deployment/Creating of new deployment ended (succesfully)
					//
					deplEvents[string(deployment.UID)]["state.Finalized.Code"] = "Finished"
					deplEvents[string(deployment.UID)]["state.Finalized.ts"] = time.Now().UTC()
					deplEvents[string(deployment.UID)]["readyToPush"] = true

					//Here we can send the event
					tags["name"] = deplEvents[string(deployment.UID)]["name"].(string)
					tags["namespace"] = deplEvents[string(deployment.UID)]["namespace"].(string)
					tags["uid"] = string(deployment.UID)

					tags["location"] = deplEvents[string(deployment.UID)]["location"].(string)
					tags["transaction"]	= deplEvents[string(deployment.UID)]["state.Code"].(string)
					tags["transaction_start_ts"] = deplEvents[string(deployment.UID)]["state.ts"].(time.Time).Format(time.RFC3339)
					tags["transaction_end_ts"] = deplEvents[string(deployment.UID)]["state.Finalized.ts"].(time.Time).Format(time.RFC3339)

					field["duration"] = deplEvents[string(deployment.UID)]["state.Finalized.ts"].(time.Time).Sub(deplEvents[string(deployment.UID)]["state.ts"].(time.Time)).Seconds()

					k8sEL.acc.AddFields(fmt.Sprintf("deployment_events"), field, tags, time.Now().UTC())
					log.Printf("W! [inputs.%s] Catched finish of upgrade or initial deploy of '%s'\n",inputName, deplEvents[string(deployment.UID)]["location"])

				}

				dif,objSpecDiff = jsondiff.Compare(deplEvents[string(deployment.UID)]["spec"].([]byte),currentObjSpecTxt,&jsonDiffOptions)
				if dif.String() == "FullMatch" {objSpecDiff = ""}
				deplEvents[string(deployment.UID)]["spec"] = []byte{}
				deplEvents[string(deployment.UID)]["spec"] = append(currentObjSpecTxt[:0:0], currentObjSpecTxt...)

				log.Printf("W! [inputs.%s] >>>>>>>>>>>>>>>>>>>>>>>>>\n",inputName)
				log.Printf("W! [inputs.%s] evt: %v, %s %s \n  specDiff: %s\n-------------------\n",
					inputName,event.Type,time.Now().Format(time.RFC3339),getObjectStringRep(deployment),
					objSpecDiff)
				log.Printf("D! [inputs.%s] Event table: %v\n",inputName,deplEvents)
				log.Printf("W! [inputs.%s] <<<<<<<<<<<<<<<<<<<<<<<\n",inputName)


			}

		}

		if *lock != *zeroLock{
			lock.Unlock()
		}

	}


}

func (k8sEL *k8sEventsListener) Start(acc telegraf.Accumulator) error {
	var err error

	if k8sEL.Mode == "Out-cluster" {
		if k8sEL.KubeConfigFile == ""{
			k8sEL.KubeConfigFile = filepath.Join(os.Getenv("HOME"), ".kube", "config")
		}

		flag.Parse()
		k8sEL.config, err = k8sClientCmd.BuildConfigFromFlags("", k8sEL.KubeConfigFile)
		if err != nil {
			return err
		}

	} else if k8sEL.Mode == "In-cluster" {
		k8sEL.config, err = k8sRest.InClusterConfig()
		if err != nil {
			return err
		}
	} else {return errors.New(fmt.Sprintf("Not supported mode '%s'",k8sEL.Mode))}

	k8sEL.clientSet, err = k8s.NewForConfig(k8sEL.config)
	if err != nil {
		return err
	}

	apiCoreV1.AddToScheme(k8sScheme.Scheme)
	apiAppsV1.AddToScheme(k8sScheme.Scheme)
	k8sEL.serializer = jSerializer.NewSerializer(jSerializer.DefaultMetaFactory,k8sScheme.Scheme,k8sScheme.Scheme,true)

	k8sEL.acc = acc

	//Create wathcers, start listening
	k8sEL.watchers = map[string]k8sWatch.Interface{}
	k8sEL.eventsTable = map[string]map[string]map[string]interface{}{}
	k8sEL.lockers = map[string]*sync.RWMutex{}

	for _,object := range k8sEL.TargetObjects {
		switch object {
			case "Deployment" :

				k8sEL.watchers[object], err = k8sEL.clientSet.AppsV1().Deployments(k8sEL.Namespace).Watch(metav1.ListOptions{})
				if err != nil {
					return errors.New(fmt.Sprintf("Can't create '%s' watcher. Reason: %v\n",object,err))
				}

				k8sEL.eventsTable[object] = map[string]map[string]interface{}{}
				k8sEL.lockers[object] = &sync.RWMutex{}


				go k8sEL.deploymentEventsListener(k8sEL.watchers[object],k8sEL.eventsTable[object],k8sEL.lockers[object])
		}
	}
	return nil
}


func (k8sEL *k8sEventsListener) Stop() {
	for _,object := range k8sEL.TargetObjects {
		log.Printf("I! [inputs.%s] Stopping '%s' watcher...",inputName, object)
		k8sEL.watchers[object].Stop()
	}

}

func init() {
	inputs.Add(inputName, func() telegraf.Input {return &k8sEventsListener{}})
}