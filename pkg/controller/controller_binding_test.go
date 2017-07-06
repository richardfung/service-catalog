/*
Copyright 2017 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kubernetes-incubator/service-catalog/pkg/apis/servicecatalog/v1alpha1"
	osb "github.com/rifung/go-open-service-broker-client/v2"
	fakeosb "github.com/rifung/go-open-service-broker-client/v2/fake"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"k8s.io/client-go/pkg/api"
	"k8s.io/client-go/pkg/api/v1"
	settingsv1alpha1 "k8s.io/client-go/pkg/apis/settings/v1alpha1"
	clientgotesting "k8s.io/client-go/testing"
)

func TestReconcileBindingNonExistingInstance(t *testing.T) {
	_, fakeCatalogClient, fakeBrokerClient, testController, _ := newTestController(t, noFakeActions())

	binding := &v1alpha1.Binding{
		ObjectMeta: metav1.ObjectMeta{Name: testBindingName},
		Spec: v1alpha1.BindingSpec{
			InstanceRef: v1.LocalObjectReference{Name: "nothere"},
			ExternalID:  bindingGUID,
		},
	}

	testController.reconcileBinding(binding)

	brokerActions := fakeBrokerClient.Actions()
	assertNumberOfBrokerActions(t, brokerActions, 0)

	actions := fakeCatalogClient.Actions()
	assertNumberOfActions(t, actions, 1)

	// There should only be one action that says it failed because no such instance exists.
	updateAction := actions[0].(clientgotesting.UpdateAction)
	if e, a := "update", updateAction.GetVerb(); e != a {
		t.Fatalf("Unexpected verb on actions[0]; expected %v, got %v", e, a)
	}
	updatedBinding := assertUpdateStatus(t, actions[0], binding)
	assertBindingReadyFalse(t, updatedBinding, errorNonexistentInstanceReason)

	events := getRecordedEvents(testController)
	assertNumEvents(t, events, 1)

	expectedEvent := api.EventTypeWarning + " " + errorNonexistentInstanceReason + " " + "Binding \"/test-binding\" references a non-existent Instance \"/nothere\""
	if e, a := expectedEvent, events[0]; e != a {
		t.Fatalf("Received unexpected event: %v", a)
	}
}

func TestReconcileBindingNonExistingServiceClass(t *testing.T) {
	_, fakeCatalogClient, fakeBrokerClient, testController, sharedInformers := newTestController(t, noFakeActions())

	sharedInformers.Brokers().Informer().GetStore().Add(getTestBroker())
	sharedInformers.ServiceClasses().Informer().GetStore().Add(getTestServiceClass())
	instance := &v1alpha1.Instance{
		ObjectMeta: metav1.ObjectMeta{Name: testInstanceName, Namespace: testNamespace},
		Spec: v1alpha1.InstanceSpec{
			ServiceClassName: "nothere",
			PlanName:         testPlanName,
			ExternalID:       instanceGUID,
		},
	}
	sharedInformers.Instances().Informer().GetStore().Add(instance)

	binding := &v1alpha1.Binding{
		ObjectMeta: metav1.ObjectMeta{Name: testBindingName, Namespace: testNamespace},
		Spec: v1alpha1.BindingSpec{
			InstanceRef: v1.LocalObjectReference{Name: testInstanceName},
			ExternalID:  bindingGUID,
		},
	}

	testController.reconcileBinding(binding)

	brokerActions := fakeBrokerClient.Actions()
	assertNumberOfBrokerActions(t, brokerActions, 0)

	actions := fakeCatalogClient.Actions()
	assertNumberOfActions(t, actions, 1)

	// There should only be one action that says it failed because no such service class.
	updatedBinding := assertUpdateStatus(t, actions[0], binding)
	assertBindingReadyFalse(t, updatedBinding, errorNonexistentServiceClassMessage)

	events := getRecordedEvents(testController)
	assertNumEvents(t, events, 1)

	expectedEvent := api.EventTypeWarning + " " + errorNonexistentServiceClassMessage + " " + "Binding \"test-ns/test-binding\" references a non-existent ServiceClass \"nothere\""
	if e, a := expectedEvent, events[0]; e != a {
		t.Fatalf("Received unexpected event: %v", a)
	}
}

func TestReconcileBindingWithParameters(t *testing.T) {
	fakeKubeClient, fakeCatalogClient, fakeBrokerClient, testController, sharedInformers := newTestController(t, fakeosb.FakeClientConfiguration{
		BindReaction: &fakeosb.BindReaction{
			Response: &osb.BindResponse{
				Credentials: map[string]interface{}{
					"a": "b",
					"c": "d",
				},
			},
		},
	})

	addGetNamespaceReaction(fakeKubeClient)
	addGetSecretNotFoundReaction(fakeKubeClient)

	sharedInformers.Brokers().Informer().GetStore().Add(getTestBroker())
	sharedInformers.ServiceClasses().Informer().GetStore().Add(getTestServiceClass())
	sharedInformers.Instances().Informer().GetStore().Add(getTestInstanceWithStatus(v1alpha1.ConditionTrue))

	binding := &v1alpha1.Binding{
		ObjectMeta: metav1.ObjectMeta{Name: testBindingName, Namespace: testNamespace},
		Spec: v1alpha1.BindingSpec{
			InstanceRef: v1.LocalObjectReference{Name: testInstanceName},
			ExternalID:  bindingGUID,
			SecretName:  testBindingSecretName,
		},
	}

	parameters := bindingParameters{Name: "test-param"}
	parameters.Args = append(parameters.Args, "first-arg")
	parameters.Args = append(parameters.Args, "second-arg")
	b, err := json.Marshal(parameters)
	if err != nil {
		t.Fatalf("Failed to marshal parameters %v : %v", parameters, err)
	}
	binding.Spec.Parameters = &runtime.RawExtension{Raw: b}

	testController.reconcileBinding(binding)

	brokerActions := fakeBrokerClient.Actions()
	assertNumberOfBrokerActions(t, brokerActions, 1)
	assertBind(t, brokerActions[0], &osb.BindRequest{
		BindingID:  bindingGUID,
		InstanceID: instanceGUID,
		ServiceID:  serviceClassGUID,
		PlanID:     planGUID,
		AppGUID:    strPtr(testNsUID),
		Parameters: map[string]interface{}{
			"args": []interface{}{
				"first-arg",
				"second-arg",
			},
			"name": "test-param",
		},
		BindResource: &osb.BindResource{
			AppGUID: strPtr(testNsUID),
		},
	})

	actions := fakeCatalogClient.Actions()
	assertNumberOfActions(t, actions, 1)
	updatedBinding := assertUpdateStatus(t, actions[0], binding)
	assertBindingReadyTrue(t, updatedBinding)

	kubeActions := fakeKubeClient.Actions()
	assertNumberOfActions(t, kubeActions, 3)

	// first action is a get on the namespace
	// second action is a get on the secret

	action := kubeActions[2].(clientgotesting.CreateAction)
	if e, a := "create", action.GetVerb(); e != a {
		t.Fatalf("Unexpected verb on action; expected %v, got %v", e, a)
	}
	if e, a := "secrets", action.GetResource().Resource; e != a {
		t.Fatalf("Unexpected resource on action; expected %v, got %v", e, a)
	}
	actionSecret, ok := action.GetObject().(*v1.Secret)
	if !ok {
		t.Fatal("couldn't convert secret into a v1.Secret")
	}
	if e, a := testBindingSecretName, actionSecret.Name; e != a {
		t.Fatalf("Unexpected name of secret; expected %v, got %v", e, a)
	}
	value, ok := actionSecret.Data["a"]
	if !ok {
		t.Fatal("Didn't find secret key 'a' in created secret")
	}
	if e, a := "b", string(value); e != a {
		t.Fatalf("Unexpected value of key 'a' in created secret; expected %v got %v", e, a)
	}
	value, ok = actionSecret.Data["c"]
	if !ok {
		t.Fatal("Didn't find secret key 'a' in created secret")
	}
	if e, a := "d", string(value); e != a {
		t.Fatalf("Unexpected value of key 'c' in created secret; expected %v got %v", e, a)
	}

	events := getRecordedEvents(testController)
	assertNumEvents(t, events, 1)

	expectedEvent := api.EventTypeNormal + " " + successInjectedBindResultReason + " " + successInjectedBindResultMessage
	if e, a := expectedEvent, events[0]; e != a {
		t.Fatalf("Received unexpected event: %v", a)
	}
}

func TestReconcileBindingNonbindableServiceClass(t *testing.T) {
	_, fakeCatalogClient, fakeBrokerClient, testController, sharedInformers := newTestController(t, noFakeActions())

	sharedInformers.Brokers().Informer().GetStore().Add(getTestBroker())
	sharedInformers.ServiceClasses().Informer().GetStore().Add(getTestNonbindableServiceClass())
	sharedInformers.Instances().Informer().GetStore().Add(getTestNonbindableInstance())

	binding := &v1alpha1.Binding{
		ObjectMeta: metav1.ObjectMeta{Name: testBindingName, Namespace: testNamespace},
		Spec: v1alpha1.BindingSpec{
			InstanceRef: v1.LocalObjectReference{Name: testInstanceName},
			ExternalID:  bindingGUID,
		},
	}

	testController.reconcileBinding(binding)

	brokerActions := fakeBrokerClient.Actions()
	assertNumberOfBrokerActions(t, brokerActions, 0)

	actions := fakeCatalogClient.Actions()
	assertNumberOfActions(t, actions, 1)

	// There should only be one action that says binding was created
	updatedBinding := assertUpdateStatus(t, actions[0], binding)
	assertBindingReadyFalse(t, updatedBinding, errorNonbindableServiceClassReason)

	events := getRecordedEvents(testController)
	assertNumEvents(t, events, 1)

	expectedEvent := api.EventTypeWarning + " " + errorNonbindableServiceClassReason + ` Binding "test-ns/test-binding" references a non-bindable ServiceClass ("test-unbindable-serviceclass") and Plan ("test-unbindable-plan") combination`
	if e, a := expectedEvent, events[0]; e != a {
		t.Fatalf("Received unexpected event: %v", a)
	}
}

func TestReconcileBindingNonbindableServiceClassBindablePlan(t *testing.T) {
	fakeKubeClient, fakeCatalogClient, fakeBrokerClient, testController, sharedInformers := newTestController(t, fakeosb.FakeClientConfiguration{
		BindReaction: &fakeosb.BindReaction{
			Response: &osb.BindResponse{
				Credentials: map[string]interface{}{
					"a": "b",
					"c": "d",
				},
			},
		},
	})

	addGetNamespaceReaction(fakeKubeClient)
	addGetSecretNotFoundReaction(fakeKubeClient)

	sharedInformers.Brokers().Informer().GetStore().Add(getTestBroker())
	sharedInformers.ServiceClasses().Informer().GetStore().Add(getTestNonbindableServiceClass())
	sharedInformers.Instances().Informer().GetStore().Add(func() *v1alpha1.Instance {
		i := getTestInstanceNonbindableServiceBindablePlan()
		i.Status = v1alpha1.InstanceStatus{
			Conditions: []v1alpha1.InstanceCondition{
				{
					Type:   v1alpha1.InstanceConditionReady,
					Status: v1alpha1.ConditionTrue,
				},
			},
		}
		return i
	}())

	binding := &v1alpha1.Binding{
		ObjectMeta: metav1.ObjectMeta{Name: testBindingName, Namespace: testNamespace},
		Spec: v1alpha1.BindingSpec{
			InstanceRef: v1.LocalObjectReference{Name: testInstanceName},
			ExternalID:  bindingGUID,
			SecretName:  testBindingSecretName,
		},
	}

	testController.reconcileBinding(binding)

	brokerActions := fakeBrokerClient.Actions()
	assertNumberOfBrokerActions(t, brokerActions, 1)
	assertBind(t, brokerActions[0], &osb.BindRequest{
		BindingID:  bindingGUID,
		InstanceID: instanceGUID,
		ServiceID:  nonbindableServiceClassGUID,
		PlanID:     planGUID,
		AppGUID:    strPtr(testNsUID),
		BindResource: &osb.BindResource{
			AppGUID: strPtr(testNsUID),
		},
	})

	actions := fakeCatalogClient.Actions()
	assertNumberOfActions(t, actions, 1)
	updatedBinding := assertUpdateStatus(t, actions[0], binding)
	assertBindingReadyTrue(t, updatedBinding)

	kubeActions := fakeKubeClient.Actions()
	assertNumberOfActions(t, kubeActions, 3)

	// first action is a get on the namespace
	// second action is a get on the secret

	action := kubeActions[2].(clientgotesting.CreateAction)
	if e, a := "create", action.GetVerb(); e != a {
		t.Fatalf("Unexpected verb on action; expected %v, got %v", e, a)
	}
	if e, a := "secrets", action.GetResource().Resource; e != a {
		t.Fatalf("Unexpected resource on action; expected %v, got %v", e, a)
	}
	actionSecret, ok := action.GetObject().(*v1.Secret)
	if !ok {
		t.Fatal("couldn't convert secret into a v1.Secret")
	}
	if e, a := testBindingSecretName, actionSecret.Name; e != a {
		t.Fatalf("Unexpected name of secret; expected %v, got %v", e, a)
	}
	value, ok := actionSecret.Data["a"]
	if !ok {
		t.Fatal("Didn't find secret key 'a' in created secret")
	}
	if e, a := "b", string(value); e != a {
		t.Fatalf("Unexpected value of key 'a' in created secret; expected %v got %v", e, a)
	}
	value, ok = actionSecret.Data["c"]
	if !ok {
		t.Fatal("Didn't find secret key 'a' in created secret")
	}
	if e, a := "d", string(value); e != a {
		t.Fatalf("Unexpected value of key 'c' in created secret; expected %v got %v", e, a)
	}

	events := getRecordedEvents(testController)
	assertNumEvents(t, events, 1)
}

func TestReconcileBindingBindableServiceClassNonbindablePlan(t *testing.T) {
	_, fakeCatalogClient, fakeBrokerClient, testController, sharedInformers := newTestController(t, noFakeActions())

	sharedInformers.Brokers().Informer().GetStore().Add(getTestBroker())
	sharedInformers.ServiceClasses().Informer().GetStore().Add(getTestServiceClass())
	sharedInformers.Instances().Informer().GetStore().Add(getTestInstanceBindableServiceNonbindablePlan())

	binding := &v1alpha1.Binding{
		ObjectMeta: metav1.ObjectMeta{Name: testBindingName, Namespace: testNamespace},
		Spec: v1alpha1.BindingSpec{
			InstanceRef: v1.LocalObjectReference{Name: testInstanceName},
			ExternalID:  bindingGUID,
		},
	}

	testController.reconcileBinding(binding)

	brokerActions := fakeBrokerClient.Actions()
	assertNumberOfBrokerActions(t, brokerActions, 0)

	actions := fakeCatalogClient.Actions()
	assertNumberOfActions(t, actions, 1)

	// There should only be one action that says binding was created
	updatedBinding := assertUpdateStatus(t, actions[0], binding)
	assertBindingReadyFalse(t, updatedBinding, errorNonbindableServiceClassReason)

	events := getRecordedEvents(testController)
	assertNumEvents(t, events, 1)

	expectedEvent := api.EventTypeWarning + " " + errorNonbindableServiceClassReason + ` Binding "test-ns/test-binding" references a non-bindable ServiceClass ("test-serviceclass") and Plan ("test-unbindable-plan") combination`
	if e, a := expectedEvent, events[0]; e != a {
		t.Fatalf("Received unexpected event: %v", a)
	}
}

func TestReconcileBindingFailsWithInstanceAsyncOngoing(t *testing.T) {
	fakeKubeClient, fakeCatalogClient, fakeBrokerClient, testController, sharedInformers := newTestController(t, noFakeActions())

	sharedInformers.Brokers().Informer().GetStore().Add(getTestBroker())
	sharedInformers.ServiceClasses().Informer().GetStore().Add(getTestServiceClass())
	sharedInformers.Instances().Informer().GetStore().Add(getTestInstanceAsyncProvisioning(""))

	binding := &v1alpha1.Binding{
		ObjectMeta: metav1.ObjectMeta{Name: testBindingName, Namespace: testNamespace},
		Spec: v1alpha1.BindingSpec{
			InstanceRef: v1.LocalObjectReference{Name: testInstanceName},
			ExternalID:  bindingGUID,
		},
	}

	err := testController.reconcileBinding(binding)
	if err == nil {
		t.Fatalf("reconcileBinding did not fail with async operation ongoing")
	}

	if !strings.Contains(err.Error(), "Ongoing Asynchronous") {
		t.Fatalf("Did not get the expected error %q : got %q", "Ongoing Asynchronous", err)
	}

	brokerActions := fakeBrokerClient.Actions()
	assertNumberOfBrokerActions(t, brokerActions, 0)

	// verify no kube resources created.
	// No actions
	kubeActions := fakeKubeClient.Actions()
	assertNumberOfActions(t, kubeActions, 0)

	actions := fakeCatalogClient.Actions()
	assertNumberOfActions(t, actions, 1)

	// There should only be one action that says binding was created
	updatedBinding := assertUpdateStatus(t, actions[0], binding)
	assertBindingReadyFalse(t, updatedBinding)

	events := getRecordedEvents(testController)
	assertNumEvents(t, events, 1)

	if !strings.Contains(events[0], "has ongoing asynchronous operation") {
		t.Fatalf("Did not find expected error %q : got %q", "has ongoing asynchronous operation", events[0])
	}
	if !strings.Contains(events[0], testNamespace+"/"+testInstanceName) {
		t.Fatalf("Did not find expected instance name : got %q", events[0])
	}
	if !strings.Contains(events[0], testNamespace+"/"+testBindingName) {
		t.Fatalf("Did not find expected binding name : got %q", events[0])
	}
}

func TestReconcileBindingInstanceNotReady(t *testing.T) {
	fakeKubeClient, fakeCatalogClient, fakeBrokerClient, testController, sharedInformers := newTestController(t, noFakeActions())

	addGetNamespaceReaction(fakeKubeClient)

	sharedInformers.Brokers().Informer().GetStore().Add(getTestBroker())
	sharedInformers.ServiceClasses().Informer().GetStore().Add(getTestServiceClass())
	sharedInformers.Instances().Informer().GetStore().Add(getTestInstance())

	binding := &v1alpha1.Binding{
		ObjectMeta: metav1.ObjectMeta{Name: testBindingName, Namespace: testNamespace},
		Spec: v1alpha1.BindingSpec{
			InstanceRef: v1.LocalObjectReference{Name: testInstanceName},
			ExternalID:  bindingGUID,
		},
	}

	testController.reconcileBinding(binding)

	brokerActions := fakeBrokerClient.Actions()
	assertNumberOfBrokerActions(t, brokerActions, 0)

	actions := fakeCatalogClient.Actions()
	assertNumberOfActions(t, actions, 1)

	// There should only be one action that says binding was created
	updatedBinding := assertUpdateStatus(t, actions[0], binding)
	assertBindingReadyFalse(t, updatedBinding)

	events := getRecordedEvents(testController)
	assertNumEvents(t, events, 1)

	expectedEvent := api.EventTypeWarning + " " + errorInstanceNotReadyReason + " " + `Binding cannot begin because referenced instance "test-ns/test-instance" is not ready`
	if e, a := expectedEvent, events[0]; e != a {
		t.Fatalf("Received unexpected event: %v", a)
	}
}

func TestReconcileBindingNamespaceError(t *testing.T) {
	fakeKubeClient, fakeCatalogClient, fakeBrokerClient, testController, sharedInformers := newTestController(t, noFakeActions())

	fakeKubeClient.AddReactor("get", "namespaces", func(action clientgotesting.Action) (bool, runtime.Object, error) {
		return true, &v1.Namespace{}, errors.New("No namespace")
	})

	sharedInformers.Brokers().Informer().GetStore().Add(getTestBroker())
	sharedInformers.ServiceClasses().Informer().GetStore().Add(getTestServiceClass())
	sharedInformers.Instances().Informer().GetStore().Add(getTestInstance())

	binding := &v1alpha1.Binding{
		ObjectMeta: metav1.ObjectMeta{Name: testBindingName, Namespace: testNamespace},
		Spec: v1alpha1.BindingSpec{
			InstanceRef: v1.LocalObjectReference{Name: testInstanceName},
			ExternalID:  bindingGUID,
		},
	}

	testController.reconcileBinding(binding)

	brokerActions := fakeBrokerClient.Actions()
	assertNumberOfBrokerActions(t, brokerActions, 0)

	actions := fakeCatalogClient.Actions()
	assertNumberOfActions(t, actions, 1)
	updatedBinding := assertUpdateStatus(t, actions[0], binding)
	assertBindingReadyFalse(t, updatedBinding)

	events := getRecordedEvents(testController)
	assertNumEvents(t, events, 1)

	expectedEvent := api.EventTypeWarning + " " + errorFindingNamespaceInstanceReason + " " + "Failed to get namespace \"test-ns\" during binding: No namespace"
	if e, a := expectedEvent, events[0]; e != a {
		t.Fatalf("Received unexpected event: %v", a)
	}
}

func TestReconcileBindingDelete(t *testing.T) {
	fakeKubeClient, fakeCatalogClient, fakeBrokerClient, testController, sharedInformers := newTestController(t, fakeosb.FakeClientConfiguration{
		UnbindReaction: &fakeosb.UnbindReaction{},
	})

	sharedInformers.Brokers().Informer().GetStore().Add(getTestBroker())
	sharedInformers.ServiceClasses().Informer().GetStore().Add(getTestServiceClass())
	sharedInformers.Instances().Informer().GetStore().Add(getTestInstance())

	binding := &v1alpha1.Binding{
		ObjectMeta: metav1.ObjectMeta{
			Name:              testBindingName,
			Namespace:         testNamespace,
			DeletionTimestamp: &metav1.Time{},
			Finalizers:        []string{v1alpha1.FinalizerServiceCatalog},
		},
		Spec: v1alpha1.BindingSpec{
			InstanceRef: v1.LocalObjectReference{Name: testInstanceName},
			ExternalID:  bindingGUID,
			SecretName:  testBindingSecretName,
		},
	}

	fakeCatalogClient.AddReactor("get", "bindings", func(action clientgotesting.Action) (bool, runtime.Object, error) {
		return true, binding, nil
	})

	testController.reconcileBinding(binding)

	brokerActions := fakeBrokerClient.Actions()
	assertNumberOfBrokerActions(t, brokerActions, 1)
	assertUnbind(t, brokerActions[0], &osb.UnbindRequest{
		BindingID:  bindingGUID,
		InstanceID: instanceGUID,
		ServiceID:  serviceClassGUID,
		PlanID:     planGUID,
	})

	kubeActions := fakeKubeClient.Actions()
	// The two actions should be:
	// 0. Deleting the secret
	assertNumberOfActions(t, kubeActions, 1)

	deleteAction := kubeActions[0].(clientgotesting.DeleteActionImpl)
	if e, a := "delete", deleteAction.GetVerb(); e != a {
		t.Fatalf("Unexpected verb on kubeActions[1]; expected %v, got %v", e, a)
	}

	if e, a := binding.Spec.SecretName, deleteAction.Name; e != a {
		t.Fatalf("Unexpected name of secret: expected %v, got %v", e, a)
	}

	actions := fakeCatalogClient.Actions()
	// The three actions should be:
	// 0. Updating the ready condition
	// 1. Get against the binding in question
	// 2. Removing the finalizer
	assertNumberOfActions(t, actions, 3)

	updatedBinding := assertUpdateStatus(t, actions[0], binding)
	assertBindingReadyFalse(t, updatedBinding)

	assertGet(t, actions[1], binding)

	updatedBinding = assertUpdateStatus(t, actions[2], binding)
	assertEmptyFinalizers(t, updatedBinding)

	events := getRecordedEvents(testController)
	assertNumEvents(t, events, 1)

	expectedEvent := api.EventTypeNormal + " " + successUnboundReason + " " + "This binding was deleted successfully"
	if e, a := expectedEvent, events[0]; e != a {
		t.Fatalf("Received unexpected event: %v", a)
	}
}

const testPodPresetName = "test-pod-preset"

func TestReconcileBindingWithPodPresetTemplate(t *testing.T) {
	fakeKubeClient, fakeCatalogClient, fakeBrokerClient, testController, sharedInformers := newTestController(t, fakeosb.FakeClientConfiguration{
		BindReaction: &fakeosb.BindReaction{
			Response: &osb.BindResponse{
				Credentials: map[string]interface{}{
					"a": "b",
					"c": "d",
				},
			},
		},
	})

	addGetNamespaceReaction(fakeKubeClient)
	addGetSecretNotFoundReaction(fakeKubeClient)

	fakeKubeClient.AddReactor("create", "podpresets", func(action clientgotesting.Action) (bool, runtime.Object, error) {
		return true, nil, nil
	})

	sharedInformers.Brokers().Informer().GetStore().Add(getTestBroker())
	sharedInformers.ServiceClasses().Informer().GetStore().Add(getTestServiceClass())
	sharedInformers.Instances().Informer().GetStore().Add(getTestInstanceWithStatus(v1alpha1.ConditionTrue))

	binding := &v1alpha1.Binding{
		ObjectMeta: metav1.ObjectMeta{Name: testBindingName, Namespace: testNamespace},
		Spec: v1alpha1.BindingSpec{
			InstanceRef: v1.LocalObjectReference{Name: testInstanceName},
			ExternalID:  bindingGUID,
			SecretName:  testBindingSecretName,
			AlphaPodPresetTemplate: &v1alpha1.AlphaPodPresetTemplate{
				Name: testPodPresetName,
				Selector: metav1.LabelSelector{
					MatchLabels: map[string]string{
						"foo": "bar",
					},
				},
			},
		},
	}

	testController.reconcileBinding(binding)

	brokerActions := fakeBrokerClient.Actions()
	assertNumberOfBrokerActions(t, brokerActions, 1)
	assertBind(t, brokerActions[0], &osb.BindRequest{
		BindingID:  bindingGUID,
		InstanceID: instanceGUID,
		ServiceID:  serviceClassGUID,
		PlanID:     planGUID,
		AppGUID:    strPtr(testNsUID),
		BindResource: &osb.BindResource{
			AppGUID: strPtr(testNsUID),
		},
	})

	actions := fakeCatalogClient.Actions()
	assertNumberOfActions(t, actions, 1)

	// There should only be one action that says binding was created
	updatedBinding := assertUpdateStatus(t, actions[0], binding)
	assertBindingReadyTrue(t, updatedBinding)

	kubeActions := fakeKubeClient.Actions()
	assertNumberOfActions(t, kubeActions, 4)

	action := kubeActions[2].(clientgotesting.CreateAction)
	if e, a := "create", action.GetVerb(); e != a {
		t.Fatalf("Unexpected verb on action; expected %v, got %v", e, a)
	}
	if e, a := "secrets", action.GetResource().Resource; e != a {
		t.Fatalf("Unexpected resource on action; expected %v, got %v", e, a)
	}
	actionSecret, ok := action.GetObject().(*v1.Secret)
	if !ok {
		t.Fatal("couldn't convert secret into a v1.Secret")
	}
	if e, a := testBindingSecretName, actionSecret.Name; e != a {
		t.Fatalf("Unexpected name of secret; expected %v, got %v", e, a)
	}

	action = kubeActions[3].(clientgotesting.CreateAction)
	if e, a := "create", action.GetVerb(); e != a {
		t.Fatalf("Unexpected verb on action; expected %v, got %v", e, a)
	}
	if e, a := "podpresets", action.GetResource().Resource; e != a {
		t.Fatalf("Unexpected resource on action; expected %v, got %v", e, a)
	}
	actionPodPreset, ok := action.GetObject().(*settingsv1alpha1.PodPreset)
	if !ok {
		t.Fatal("couldn't convert PodPreset into a settingsv1alpha1.PodPreset")
	}
	if e, a := testPodPresetName, actionPodPreset.Name; e != a {
		t.Fatalf("Unexpected name of PodPreset; expected %v, got %v", e, a)
	}

	events := getRecordedEvents(testController)
	assertNumEvents(t, events, 1)

	expectedEvent := api.EventTypeNormal + " " + successInjectedBindResultReason + " " + successInjectedBindResultMessage
	if e, a := expectedEvent, events[0]; e != a {
		t.Fatalf("Received unexpected event: %v", a)
	}
}

func TestUpdateBindingCondition(t *testing.T) {
	getTestBindingWithStatus := func(status v1alpha1.ConditionStatus) *v1alpha1.Binding {
		instance := getTestBinding()
		instance.Status = v1alpha1.BindingStatus{
			Conditions: []v1alpha1.BindingCondition{{
				Type:               v1alpha1.BindingConditionReady,
				Status:             status,
				Message:            "message",
				LastTransitionTime: metav1.NewTime(time.Now().Add(-5 * time.Minute)),
			}},
		}

		return instance
	}

	cases := []struct {
		name                  string
		input                 *v1alpha1.Binding
		status                v1alpha1.ConditionStatus
		reason                string
		message               string
		transitionTimeChanged bool
	}{

		{
			name:                  "initially unset",
			input:                 getTestBinding(),
			status:                v1alpha1.ConditionFalse,
			transitionTimeChanged: true,
		},
		{
			name:                  "not ready -> not ready",
			input:                 getTestBindingWithStatus(v1alpha1.ConditionFalse),
			status:                v1alpha1.ConditionFalse,
			transitionTimeChanged: false,
		},
		{
			name:                  "not ready -> not ready, message and reason change",
			input:                 getTestBindingWithStatus(v1alpha1.ConditionFalse),
			status:                v1alpha1.ConditionFalse,
			reason:                "foo",
			message:               "bar",
			transitionTimeChanged: false,
		},
		{
			name:                  "not ready -> ready",
			input:                 getTestBindingWithStatus(v1alpha1.ConditionFalse),
			status:                v1alpha1.ConditionTrue,
			transitionTimeChanged: true,
		},
		{
			name:                  "ready -> ready",
			input:                 getTestBindingWithStatus(v1alpha1.ConditionTrue),
			status:                v1alpha1.ConditionTrue,
			transitionTimeChanged: false,
		},
		{
			name:                  "ready -> not ready",
			input:                 getTestBindingWithStatus(v1alpha1.ConditionTrue),
			status:                v1alpha1.ConditionFalse,
			transitionTimeChanged: true,
		},
	}

	for _, tc := range cases {
		_, fakeCatalogClient, _, testController, _ := newTestController(t, noFakeActions())

		clone, err := api.Scheme.DeepCopy(tc.input)
		if err != nil {
			t.Errorf("%v: deep copy failed", tc.name)
			continue
		}
		inputClone := clone.(*v1alpha1.Binding)

		err = testController.updateBindingCondition(tc.input, v1alpha1.BindingConditionReady, tc.status, tc.reason, tc.message)
		if err != nil {
			t.Errorf("%v: error updating broker condition: %v", tc.name, err)
			continue
		}

		if !reflect.DeepEqual(tc.input, inputClone) {
			t.Errorf("%v: updating broker condition mutated input: expected %v, got %v", tc.name, inputClone, tc.input)
			continue
		}

		actions := fakeCatalogClient.Actions()
		if ok := expectNumberOfActions(t, tc.name, actions, 1); !ok {
			continue
		}

		updatedBinding, ok := expectUpdateStatus(t, tc.name, actions[0], tc.input)
		if !ok {
			continue
		}

		updateActionObject, ok := updatedBinding.(*v1alpha1.Binding)
		if !ok {
			t.Errorf("%v: couldn't convert to binding", tc.name)
			continue
		}

		var initialTs metav1.Time
		if len(inputClone.Status.Conditions) != 0 {
			initialTs = inputClone.Status.Conditions[0].LastTransitionTime
		}

		if e, a := 1, len(updateActionObject.Status.Conditions); e != a {
			t.Errorf("%v: expected %v condition(s), got %v", tc.name, e, a)
		}

		outputCondition := updateActionObject.Status.Conditions[0]
		newTs := outputCondition.LastTransitionTime

		if tc.transitionTimeChanged && initialTs == newTs {
			t.Errorf("%v: transition time didn't change when it should have", tc.name)
			continue
		} else if !tc.transitionTimeChanged && initialTs != newTs {
			t.Errorf("%v: transition time changed when it shouldn't have", tc.name)
			continue
		}
		if e, a := tc.reason, outputCondition.Reason; e != "" && e != a {
			t.Errorf("%v: condition reasons didn't match; expected %v, got %v", tc.name, e, a)
			continue
		}
		if e, a := tc.message, outputCondition.Message; e != "" && e != a {
			t.Errorf("%v: condition reasons didn't match; expected %v, got %v", tc.name, e, a)
		}
	}
}
