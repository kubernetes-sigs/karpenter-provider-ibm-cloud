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

package fake

import (
	"errors"
	"testing"
)

func TestAtomicPtr(t *testing.T) {
	ptr := &AtomicPtr[int]{}

	// Test initial state
	if got := ptr.Get(); got != nil {
		t.Errorf("AtomicPtr.Get() = %v, want nil", got)
	}

	// Test Store and Get
	value := 42
	ptr.Store(&value)
	if got := ptr.Get(); got == nil || *got != 42 {
		t.Errorf("AtomicPtr.Get() = %v, want %v", got, &value)
	}

	// Test overwriting
	newValue := 84
	ptr.Store(&newValue)
	if got := ptr.Get(); got == nil || *got != 84 {
		t.Errorf("AtomicPtr.Get() after Store = %v, want %v", got, &newValue)
	}

	// Test storing nil
	ptr.Store(nil)
	if got := ptr.Get(); got != nil {
		t.Errorf("AtomicPtr.Get() after Store(nil) = %v, want nil", got)
	}
}

func TestAtomicPtrSlice(t *testing.T) {
	slice := &AtomicPtrSlice[string]{}

	// Test initial state
	if got := slice.Len(); got != 0 {
		t.Errorf("AtomicPtrSlice.Len() = %v, want 0", got)
	}

	if got := slice.Clone(); len(got) != 0 {
		t.Errorf("AtomicPtrSlice.Clone() = %v, want empty slice", got)
	}

	// Test Add
	slice.Add("first")
	slice.Add("second")

	if got := slice.Len(); got != 2 {
		t.Errorf("AtomicPtrSlice.Len() after Add = %v, want 2", got)
	}

	clone := slice.Clone()
	if len(clone) != 2 {
		t.Errorf("AtomicPtrSlice.Clone() length = %v, want 2", len(clone))
	}

	expected := []string{"first", "second"}
	for i, v := range clone {
		if v != expected[i] {
			t.Errorf("AtomicPtrSlice.Clone()[%d] = %v, want %v", i, v, expected[i])
		}
	}

	// Test Store
	newValues := []string{"third", "fourth", "fifth"}
	slice.Store(newValues)

	if got := slice.Len(); got != 3 {
		t.Errorf("AtomicPtrSlice.Len() after Store = %v, want 3", got)
	}

	clone = slice.Clone()
	for i, v := range clone {
		if v != newValues[i] {
			t.Errorf("AtomicPtrSlice.Clone()[%d] after Store = %v, want %v", i, v, newValues[i])
		}
	}

	// Test Store with empty slice
	slice.Store([]string{})
	if got := slice.Len(); got != 0 {
		t.Errorf("AtomicPtrSlice.Len() after Store([]) = %v, want 0", got)
	}
}

func TestAtomicError(t *testing.T) {
	atomicErr := &AtomicError{}

	// Test initial state
	if got := atomicErr.Get(); got != nil {
		t.Errorf("AtomicError.Get() = %v, want nil", got)
	}

	// Test Store and Get
	testErr := errors.New("test error")
	atomicErr.Store(testErr)

	got := atomicErr.Get()
	if got == nil || got.Error() != "test error" {
		t.Errorf("AtomicError.Get() = %v, want %v", got, testErr)
	}

	// Test that Get clears the error
	got = atomicErr.Get()
	if got != nil {
		t.Errorf("AtomicError.Get() after first Get() = %v, want nil", got)
	}

	// Test storing nil
	atomicErr.Store(nil)
	if result := atomicErr.Get(); result != nil {
		t.Errorf("AtomicError.Get() after Store(nil) = %v, want nil", result)
	}

	// Test multiple Store/Get cycles
	err1 := errors.New("error 1")
	err2 := errors.New("error 2")

	atomicErr.Store(err1)
	atomicErr.Store(err2) // Should overwrite err1

	got = atomicErr.Get()
	if got == nil || got.Error() != "error 2" {
		t.Errorf("AtomicError.Get() after overwrite = %v, want %v", got, err2)
	}
}

func TestMockedFunction(t *testing.T) {
	mock := &MockedFunction[string, int]{}

	// Test initial state
	if got := mock.CalledWithInput.Len(); got != 0 {
		t.Errorf("MockedFunction.CalledWithInput.Len() = %v, want 0", got)
	}

	if got := mock.Output.Get(); got != nil {
		t.Errorf("MockedFunction.Output.Get() = %v, want nil", got)
	}

	if got := mock.Error.Get(); got != nil {
		t.Errorf("MockedFunction.Error.Get() = %v, want nil", got)
	}

	// Test setting up mock
	mock.CalledWithInput.Add("input1")
	mock.CalledWithInput.Add("input2")

	output := 42
	mock.Output.Store(&output)

	testErr := errors.New("mock error")
	mock.Error.Store(testErr)

	// Verify state
	if got := mock.CalledWithInput.Len(); got != 2 {
		t.Errorf("MockedFunction.CalledWithInput.Len() = %v, want 2", got)
	}

	inputs := mock.CalledWithInput.Clone()
	expected := []string{"input1", "input2"}
	for i, input := range inputs {
		if input != expected[i] {
			t.Errorf("MockedFunction.CalledWithInput[%d] = %v, want %v", i, input, expected[i])
		}
	}

	if got := mock.Output.Get(); got == nil || *got != 42 {
		t.Errorf("MockedFunction.Output.Get() = %v, want %v", got, &output)
	}

	got := mock.Error.Get()
	if got == nil || got.Error() != "mock error" {
		t.Errorf("MockedFunction.Error.Get() = %v, want %v", got, testErr)
	}

	// Test Reset
	mock.Reset()

	if got := mock.CalledWithInput.Len(); got != 0 {
		t.Errorf("MockedFunction.CalledWithInput.Len() after Reset = %v, want 0", got)
	}

	if got := mock.Output.Get(); got != nil {
		t.Errorf("MockedFunction.Output.Get() after Reset = %v, want nil", got)
	}

	if got := mock.Error.Get(); got != nil {
		t.Errorf("MockedFunction.Error.Get() after Reset = %v, want nil", got)
	}
}
