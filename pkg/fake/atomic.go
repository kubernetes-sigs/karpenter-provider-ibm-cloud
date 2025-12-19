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
	"sync"

	"sigs.k8s.io/karpenter/pkg/utils/atomic"
)

// AtomicPtr wraps an atomic pointer for use in fake implementations
type AtomicPtr[T any] struct {
	mu    sync.RWMutex
	value *T
}

// Get returns the current value
func (a *AtomicPtr[T]) Get() *T {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.value
}

// Store sets the value
func (a *AtomicPtr[T]) Store(value *T) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.value = value
}

// AtomicPtrSlice wraps an atomic slice of pointers
type AtomicPtrSlice[T any] struct {
	slice atomic.Slice[T]
}

// Add appends a value to the slice
func (a *AtomicPtrSlice[T]) Add(value T) {
	a.slice.Add(value)
}

// Clone returns a copy of the slice
func (a *AtomicPtrSlice[T]) Clone() []T {
	var result []T
	a.slice.Range(func(item T) bool {
		result = append(result, item)
		return true
	})
	return result
}

// Len returns the length of the slice
func (a *AtomicPtrSlice[T]) Len() int {
	count := 0
	a.slice.Range(func(item T) bool {
		count++
		return true
	})
	return count
}

// Store replaces the entire slice
func (a *AtomicPtrSlice[T]) Store(values []T) {
	a.slice.Reset()
	for _, v := range values {
		a.slice.Add(v)
	}
}

// AtomicError wraps an atomic error
type AtomicError struct {
	mu    sync.RWMutex
	value error
}

// Get returns the current error and clears it
func (a *AtomicError) Get() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	err := a.value
	a.value = nil
	return err
}

// Store sets the error
func (a *AtomicError) Store(err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.value = err
}

// MockedFunction represents a mocked function call with input/output tracking
type MockedFunction[TInput any, TOutput any] struct {
	CalledWithInput AtomicPtrSlice[TInput]
	Output          AtomicPtr[TOutput]
	Error           AtomicError
}

// Reset clears all stored data
func (m *MockedFunction[TInput, TOutput]) Reset() {
	m.CalledWithInput.Store([]TInput{})
	m.Output.Store(nil)
	m.Error.Store(nil)
}
