package k8s_events_listener

import (
	"fmt"
	"github.com/pkg/errors"
	apiAppsV1 "k8s.io/api/apps/v1"
	apiCoreV1 "k8s.io/api/core/v1"
	"time"
)

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
	return fmt.Sprintf("%T,%s.%s rev: %s",obj,namespace,name,revision)
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
		(currentState == deploymentStateProgressing || (currentState == deploymentStateStable && prevEventEntry["state"] == currentState)) &&
		prevEventEntry["state"] == currentState  {
		return true
	}

	return false

}
