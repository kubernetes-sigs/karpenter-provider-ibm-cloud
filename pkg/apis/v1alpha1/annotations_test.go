/*
Copyright The Kubernetes Authors.

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

package v1alpha1

import (
	"strings"
	"testing"
)

func TestIBMNodeClassHashVersion(t *testing.T) {
	if IBMNodeClassHashVersion != "1" {
		t.Errorf("IBMNodeClassHashVersion = %v, want %v", IBMNodeClassHashVersion, "1")
	}
}

func TestAnnotationIBMNodeClassHash(t *testing.T) {
	expected := Group + "/nodeclass-hash"
	if AnnotationIBMNodeClassHash != expected {
		t.Errorf("AnnotationIBMNodeClassHash = %v, want %v", AnnotationIBMNodeClassHash, expected)
	}

	if !strings.HasPrefix(AnnotationIBMNodeClassHash, Group) {
		t.Errorf("AnnotationIBMNodeClassHash should start with Group prefix: %v", Group)
	}
}

func TestAnnotationIBMNodeClassHashVersion(t *testing.T) {
	expected := Group + "/nodeclass-hash-version"
	if AnnotationIBMNodeClassHashVersion != expected {
		t.Errorf("AnnotationIBMNodeClassHashVersion = %v, want %v", AnnotationIBMNodeClassHashVersion, expected)
	}

	if !strings.HasPrefix(AnnotationIBMNodeClassHashVersion, Group) {
		t.Errorf("AnnotationIBMNodeClassHashVersion should start with Group prefix: %v", Group)
	}
}

func TestAnnotationIBMNodeClaimImageID(t *testing.T) {
	expected := Group + "/image-id"
	if AnnotationIBMNodeClaimImageID != expected {
		t.Errorf("AnnotationIBMNodeClaimImageID = %v, want %v", AnnotationIBMNodeClaimImageID, expected)
	}

	if !strings.HasPrefix(AnnotationIBMNodeClaimImageID, Group) {
		t.Errorf("AnnotationIBMNodeClaimImageID should start with Group prefix: %v", Group)
	}
}

func TestAnnotationConstants(t *testing.T) {
	annotations := []string{
		AnnotationIBMNodeClassHash,
		AnnotationIBMNodeClassHashVersion,
		AnnotationIBMNodeClaimImageID,
	}

	for _, annotation := range annotations {
		if annotation == "" {
			t.Error("Annotation constant should not be empty")
		}
		if !strings.Contains(annotation, "/") {
			t.Errorf("Annotation %v should contain '/' separator", annotation)
		}
	}
}
