/*
Copyright 2014 The Kubernetes Authors All rights reserved.

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

package pod

import (
	"fmt"
	"net"
	"net/http"
	"net/url"

	"github.com/GoogleCloudPlatform/kubernetes/pkg/api"
	"github.com/GoogleCloudPlatform/kubernetes/pkg/api/errors"
	"github.com/GoogleCloudPlatform/kubernetes/pkg/api/validation"
	"github.com/GoogleCloudPlatform/kubernetes/pkg/client"
	"github.com/GoogleCloudPlatform/kubernetes/pkg/fields"
	"github.com/GoogleCloudPlatform/kubernetes/pkg/labels"
	"github.com/GoogleCloudPlatform/kubernetes/pkg/registry/generic"
	"github.com/GoogleCloudPlatform/kubernetes/pkg/runtime"
	"github.com/GoogleCloudPlatform/kubernetes/pkg/util"
	"github.com/GoogleCloudPlatform/kubernetes/pkg/util/fielderrors"
)

// podStrategy implements behavior for Pods
type podStrategy struct {
	runtime.ObjectTyper
	api.NameGenerator
}

// Strategy is the default logic that applies when creating and updating Pod
// objects via the REST API.
var Strategy = podStrategy{api.Scheme, api.SimpleNameGenerator}

// NamespaceScoped is true for pods.
func (podStrategy) NamespaceScoped() bool {
	return true
}

// PrepareForCreate clears fields that are not allowed to be set by end users on creation.
func (podStrategy) PrepareForCreate(obj runtime.Object) {
	pod := obj.(*api.Pod)
	pod.Status = api.PodStatus{
		Phase: api.PodPending,
	}
}

// PrepareForUpdate clears fields that are not allowed to be set by end users on update.
func (podStrategy) PrepareForUpdate(obj, old runtime.Object) {
	newPod := obj.(*api.Pod)
	oldPod := old.(*api.Pod)
	newPod.Status = oldPod.Status
}

// Validate validates a new pod.
func (podStrategy) Validate(ctx api.Context, obj runtime.Object) fielderrors.ValidationErrorList {
	pod := obj.(*api.Pod)
	return validation.ValidatePod(pod)
}

// AllowCreateOnUpdate is false for pods.
func (podStrategy) AllowCreateOnUpdate() bool {
	return false
}

// ValidateUpdate is the default update validation for an end user.
func (podStrategy) ValidateUpdate(ctx api.Context, obj, old runtime.Object) fielderrors.ValidationErrorList {
	errorList := validation.ValidatePod(obj.(*api.Pod))
	return append(errorList, validation.ValidatePodUpdate(obj.(*api.Pod), old.(*api.Pod))...)
}

// CheckGracefulDelete allows a pod to be gracefully deleted. It updates the DeleteOptions to
// reflect the desired grace value.
func (podStrategy) CheckGracefulDelete(obj runtime.Object, options *api.DeleteOptions) bool {
	if options == nil {
		return false
	}
	period := int64(0)
	// user has specified a value
	if options.GracePeriodSeconds != nil {
		period = *options.GracePeriodSeconds
	} else {
		// use the default value if set, or deletes the pod immediately (0)
		pod := obj.(*api.Pod)
		if pod.Spec.TerminationGracePeriodSeconds != nil {
			period = *pod.Spec.TerminationGracePeriodSeconds
		}
	}
	// ensure the options and the pod are in sync
	options.GracePeriodSeconds = &period
	return true
}

type podStatusStrategy struct {
	podStrategy
}

var StatusStrategy = podStatusStrategy{Strategy}

func (podStatusStrategy) PrepareForUpdate(obj, old runtime.Object) {
	newPod := obj.(*api.Pod)
	oldPod := old.(*api.Pod)
	newPod.Spec = oldPod.Spec
	newPod.DeletionTimestamp = nil
}

func (podStatusStrategy) ValidateUpdate(ctx api.Context, obj, old runtime.Object) fielderrors.ValidationErrorList {
	// TODO: merge valid fields after update
	return validation.ValidatePodStatusUpdate(obj.(*api.Pod), old.(*api.Pod))
}

// MatchPod returns a generic matcher for a given label and field selector.
func MatchPod(label labels.Selector, field fields.Selector) generic.Matcher {
	return &generic.SelectionPredicate{
		Label: label,
		Field: field,
		GetAttrs: func(obj runtime.Object) (labels.Set, fields.Set, error) {
			pod, ok := obj.(*api.Pod)
			if !ok {
				return nil, nil, fmt.Errorf("not a pod")
			}
			return labels.Set(pod.ObjectMeta.Labels), PodToSelectableFields(pod), nil
		},
	}
}

// PodToSelectableFields returns a label set that represents the object
// TODO: fields are not labels, and the validation rules for them do not apply.
func PodToSelectableFields(pod *api.Pod) fields.Set {
	return fields.Set{
		"metadata.name": pod.Name,
		"spec.host":     pod.Spec.NodeName,
		"status.phase":  string(pod.Status.Phase),
	}
}

// ResourceGetter is an interface for retrieving resources by ResourceLocation.
type ResourceGetter interface {
	Get(api.Context, string) (runtime.Object, error)
}

func getPod(getter ResourceGetter, ctx api.Context, name string) (*api.Pod, error) {
	obj, err := getter.Get(ctx, name)
	if err != nil {
		return nil, err
	}
	pod := obj.(*api.Pod)
	if pod == nil {
		return nil, fmt.Errorf("Unexpected object type: %#v", pod)
	}
	return pod, nil
}

// ResourceLocation returns a URL to which one can send traffic for the specified pod.
func ResourceLocation(getter ResourceGetter, ctx api.Context, id string) (*url.URL, http.RoundTripper, error) {
	// Allow ID as "podname" or "podname:port".  If port is not specified,
	// try to use the first defined port on the pod.
	name, port, valid := util.SplitPort(id)
	if !valid {
		return nil, nil, errors.NewBadRequest(fmt.Sprintf("invalid pod request %q", id))
	}
	// TODO: if port is not a number but a "(container)/(portname)", do a name lookup.

	pod, err := getPod(getter, ctx, name)
	if err != nil {
		return nil, nil, err
	}

	// Try to figure out a port.
	if port == "" {
		for i := range pod.Spec.Containers {
			if len(pod.Spec.Containers[i].Ports) > 0 {
				port = fmt.Sprintf("%d", pod.Spec.Containers[i].Ports[0].ContainerPort)
				break
			}
		}
	}

	// We leave off the scheme ('http://') because we have no idea what sort of server
	// is listening at this endpoint.
	loc := &url.URL{}
	if port == "" {
		loc.Host = pod.Status.PodIP
	} else {
		loc.Host = net.JoinHostPort(pod.Status.PodIP, port)
	}
	return loc, nil, nil
}

// LogLocation returns a the log URL for a pod container. If opts.Container is blank
// and only one container is present in the pod, that container is used.
func LogLocation(getter ResourceGetter, connInfo client.ConnectionInfoGetter, ctx api.Context, name string, opts *api.PodLogOptions) (*url.URL, http.RoundTripper, error) {

	pod, err := getPod(getter, ctx, name)
	if err != nil {
		return nil, nil, err
	}

	// Try to figure out a container
	container := opts.Container
	if container == "" {
		if len(pod.Spec.Containers) == 1 {
			container = pod.Spec.Containers[0].Name
		} else {
			return nil, nil, errors.NewBadRequest(fmt.Sprintf("a container name must be specified for pod %s", name))
		}
	}
	nodeHost := pod.Spec.NodeName
	if len(nodeHost) == 0 {
		// If pod has not been assigned a host, return an empty location
		return nil, nil, nil
	}
	nodeScheme, nodePort, nodeTransport, err := connInfo.GetConnectionInfo(nodeHost)
	if err != nil {
		return nil, nil, err
	}
	params := url.Values{}
	if opts.Follow {
		params.Add("follow", "true")
	}
	if opts.Previous {
		params.Add("previous", "true")
	}
	loc := &url.URL{
		Scheme:   nodeScheme,
		Host:     fmt.Sprintf("%s:%d", nodeHost, nodePort),
		Path:     fmt.Sprintf("/containerLogs/%s/%s/%s", pod.Namespace, name, container),
		RawQuery: params.Encode(),
	}
	return loc, nodeTransport, nil
}

// ExecLocation returns the exec URL for a pod container. If opts.Container is blank
// and only one container is present in the pod, that container is used.
func ExecLocation(getter ResourceGetter, connInfo client.ConnectionInfoGetter, ctx api.Context, name string, opts *api.PodExecOptions) (*url.URL, http.RoundTripper, error) {

	pod, err := getPod(getter, ctx, name)
	if err != nil {
		return nil, nil, err
	}

	// Try to figure out a container
	container := opts.Container
	if container == "" {
		if len(pod.Spec.Containers) == 1 {
			container = pod.Spec.Containers[0].Name
		} else {
			return nil, nil, errors.NewBadRequest(fmt.Sprintf("a container name must be specified for pod %s", name))
		}
	}
	nodeHost := pod.Spec.NodeName
	if len(nodeHost) == 0 {
		// If pod has not been assigned a host, return an empty location
		return nil, nil, fmt.Errorf("pod %s does not have a host assigned", name)
	}
	nodeScheme, nodePort, nodeTransport, err := connInfo.GetConnectionInfo(nodeHost)
	if err != nil {
		return nil, nil, err
	}
	params := url.Values{}
	if opts.Stdin {
		params.Add(api.ExecStdinParam, "1")
	}
	if opts.Stdout {
		params.Add(api.ExecStdoutParam, "1")
	}
	if opts.Stderr {
		params.Add(api.ExecStderrParam, "1")
	}
	if opts.TTY {
		params.Add(api.ExecTTYParam, "1")
	}
	for _, c := range opts.Command {
		params.Add("command", c)
	}
	loc := &url.URL{
		Scheme:   nodeScheme,
		Host:     fmt.Sprintf("%s:%d", nodeHost, nodePort),
		Path:     fmt.Sprintf("/exec/%s/%s/%s", pod.Namespace, name, container),
		RawQuery: params.Encode(),
	}
	return loc, nodeTransport, nil
}

// PortForwardLocation returns a the port-forward URL for a pod.
func PortForwardLocation(getter ResourceGetter, connInfo client.ConnectionInfoGetter, ctx api.Context, name string) (*url.URL, http.RoundTripper, error) {

	pod, err := getPod(getter, ctx, name)
	if err != nil {
		return nil, nil, err
	}

	nodeHost := pod.Spec.NodeName
	if len(nodeHost) == 0 {
		// If pod has not been assigned a host, return an empty location
		return nil, nil, errors.NewBadRequest(fmt.Sprintf("pod %s does not have a host assigned", name))
	}
	nodeScheme, nodePort, nodeTransport, err := connInfo.GetConnectionInfo(nodeHost)
	if err != nil {
		return nil, nil, err
	}
	loc := &url.URL{
		Scheme: nodeScheme,
		Host:   fmt.Sprintf("%s:%d", nodeHost, nodePort),
		Path:   fmt.Sprintf("/portForward/%s/%s", pod.Namespace, name),
	}
	return loc, nodeTransport, nil
}
