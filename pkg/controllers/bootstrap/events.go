/*
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

package bootstrap

//+kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch

import (
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/events"
)

type eventRecorder struct{ events.EventRecorder }

func (r eventRecorder) Event(object runtime.Object, eventType, reason, message string) {
	r.EventRecorder.Eventf(object, nil, eventType, reason, reason, "%s", message)
}

func (r eventRecorder) Eventf(object runtime.Object, eventType, reason, format string, args ...interface{}) {
	r.EventRecorder.Eventf(object, nil, eventType, reason, reason, format, args...)
}

func (r eventRecorder) AnnotatedEventf(object runtime.Object, _ map[string]string, eventType, reason, format string, args ...interface{}) {
	r.Eventf(object, eventType, reason, format, args...)
}
