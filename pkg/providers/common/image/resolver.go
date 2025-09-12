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

package image

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/IBM/vpc-go-sdk/vpcv1"
	"github.com/pfeifferj/karpenter-provider-ibm-cloud/pkg/cloudprovider/ibm"
)

// Resolver handles image resolution, supporting both image IDs and names
type Resolver struct {
	vpcClient *ibm.VPCClient
	region    string
}

// NewResolver creates a new image resolver
func NewResolver(vpcClient *ibm.VPCClient, region string) *Resolver {
	return &Resolver{
		vpcClient: vpcClient,
		region:    region,
	}
}

// ResolveImage resolves an image identifier (ID or name) to an image ID
func (r *Resolver) ResolveImage(ctx context.Context, imageIdentifier string) (string, error) {
	if imageIdentifier == "" {
		return "", fmt.Errorf("image identifier cannot be empty")
	}

	// If it looks like an image ID, try to get it directly
	if isImageID(imageIdentifier) {
		_, err := r.vpcClient.GetImage(ctx, imageIdentifier)
		if err == nil {
			return imageIdentifier, nil
		}
		return "", fmt.Errorf("image ID %s not found: %w", imageIdentifier, err)
	}

	// Otherwise, treat it as an image name and resolve it
	return r.resolveImageByName(ctx, imageIdentifier)
}

// resolveImageByName resolves an image name to an image ID
func (r *Resolver) resolveImageByName(ctx context.Context, imageName string) (string, error) {
	// List all images with filtering
	options := &vpcv1.ListImagesOptions{
		Visibility: stringPtr("public"), // Focus on public images first
	}

	images, err := r.vpcClient.ListImages(ctx, options)
	if err != nil {
		return "", fmt.Errorf("listing images: %w", err)
	}

	// Find matching images
	var candidates []vpcv1.Image
	for _, image := range images.Images {
		if image.Name != nil && matchesImageName(*image.Name, imageName) {
			candidates = append(candidates, image)
		}
	}

	if len(candidates) == 0 {
		// Try private images if no public images found
		options.Visibility = stringPtr("private")
		images, err := r.vpcClient.ListImages(ctx, options)
		if err != nil {
			return "", fmt.Errorf("listing private images: %w", err)
		}

		for _, image := range images.Images {
			if image.Name != nil && matchesImageName(*image.Name, imageName) {
				candidates = append(candidates, image)
			}
		}
	}

	if len(candidates) == 0 {
		return "", fmt.Errorf("no image found matching name '%s'", imageName)
	}

	// Sort candidates by creation date (newest first)
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].CreatedAt == nil || candidates[j].CreatedAt == nil {
			return false
		}
		timeI, errI := time.Parse(time.RFC3339, candidates[i].CreatedAt.String())
		timeJ, errJ := time.Parse(time.RFC3339, candidates[j].CreatedAt.String())
		if errI != nil || errJ != nil {
			return false
		}
		return timeI.After(timeJ)
	})

	// Return the most recent matching image
	if candidates[0].ID == nil {
		return "", fmt.Errorf("image %s has no ID", *candidates[0].Name)
	}

	return *candidates[0].ID, nil
}

// ListAvailableImages lists all available images with optional filtering
func (r *Resolver) ListAvailableImages(ctx context.Context, nameFilter string) ([]ImageInfo, error) {
	options := &vpcv1.ListImagesOptions{
		Visibility: stringPtr("public"),
	}

	images, err := r.vpcClient.ListImages(ctx, options)
	if err != nil {
		return nil, fmt.Errorf("listing images: %w", err)
	}

	var result []ImageInfo
	for _, image := range images.Images {
		if image.Name == nil || image.ID == nil {
			continue
		}

		// Apply name filter if provided
		if nameFilter != "" && !strings.Contains(strings.ToLower(*image.Name), strings.ToLower(nameFilter)) {
			continue
		}

		info := ImageInfo{
			ID:   *image.ID,
			Name: *image.Name,
		}

		if image.CreatedAt != nil {
			if createdAt, err := time.Parse(time.RFC3339, image.CreatedAt.String()); err == nil {
				info.CreatedAt = createdAt
			}
		}

		if image.OperatingSystem != nil && image.OperatingSystem.Name != nil {
			info.OperatingSystem = *image.OperatingSystem.Name
		}

		if image.Status != nil {
			info.Status = *image.Status
		}

		result = append(result, info)
	}

	// Sort by creation date (newest first)
	sort.Slice(result, func(i, j int) bool {
		return result[i].CreatedAt.After(result[j].CreatedAt)
	})

	return result, nil
}

// ImageInfo contains information about an image
type ImageInfo struct {
	ID              string
	Name            string
	CreatedAt       time.Time
	OperatingSystem string
	Status          string
}

// isImageID checks if a string looks like an IBM Cloud image ID
func isImageID(s string) bool {
	// IBM Cloud image IDs start with "r" followed by region code and UUID
	// Format: r<region>-<uuid>
	// Examples: r006-72b27b5c-f4b0-48bb-b954-5becc7c1dcb8
	if len(s) < 10 {
		return false
	}

	if !strings.HasPrefix(s, "r") {
		return false
	}

	parts := strings.Split(s, "-")
	if len(parts) < 6 { // r006-uuid (5 parts for UUID)
		return false
	}

	// Check if the first part is r + digits
	if len(parts[0]) < 2 {
		return false
	}

	for _, char := range parts[0][1:] {
		if char < '0' || char > '9' {
			return false
		}
	}

	return true
}

// matchesImageName checks if an image name matches a given pattern
func matchesImageName(imageName, pattern string) bool {
	imageName = strings.ToLower(imageName)
	pattern = strings.ToLower(pattern)

	// Exact match
	if imageName == pattern {
		return true
	}

	// Contains match
	if strings.Contains(imageName, pattern) {
		return true
	}

	// Pattern match with wildcards
	if strings.Contains(pattern, "*") {
		return matchesPattern(imageName, pattern)
	}

	return false
}

// matchesPattern performs simple wildcard matching
func matchesPattern(s, pattern string) bool {
	// Simple wildcard matching
	if pattern == "*" {
		return true
	}

	if strings.HasPrefix(pattern, "*") && strings.HasSuffix(pattern, "*") {
		// *substring*
		return strings.Contains(s, pattern[1:len(pattern)-1])
	}

	if strings.HasPrefix(pattern, "*") {
		// *suffix
		return strings.HasSuffix(s, pattern[1:])
	}

	if strings.HasSuffix(pattern, "*") {
		// prefix*
		return strings.HasPrefix(s, pattern[:len(pattern)-1])
	}

	return s == pattern
}

// stringPtr returns a pointer to a string
func stringPtr(s string) *string {
	return &s
}
