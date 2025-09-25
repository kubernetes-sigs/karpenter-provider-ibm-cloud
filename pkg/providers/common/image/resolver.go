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
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/IBM/vpc-go-sdk/vpcv1"
	"github.com/go-logr/logr"
	"github.com/pfeifferj/karpenter-provider-ibm-cloud/pkg/apis/v1alpha1"
	"github.com/pfeifferj/karpenter-provider-ibm-cloud/pkg/cloudprovider/ibm"
)

// Resolver handles image resolution, supporting both image IDs and names
type Resolver struct {
	vpcClient *ibm.VPCClient
	region    string
	logger    logr.Logger
}

// NewResolver creates a new image resolver
func NewResolver(vpcClient *ibm.VPCClient, region string, logger logr.Logger) *Resolver {
	return &Resolver{
		vpcClient: vpcClient,
		region:    region,
		logger:    logger,
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

// ResolveImageBySelector resolves an image using semantic selection criteria
// Returns the most recent image that matches all specified criteria
func (r *Resolver) ResolveImageBySelector(ctx context.Context, selector *v1alpha1.ImageSelector) (string, error) {
	if selector == nil {
		return "", fmt.Errorf("image selector cannot be nil")
	}

	r.logger.Info("Starting image resolution by selector",
		"os", selector.OS,
		"majorVersion", selector.MajorVersion,
		"minorVersion", selector.MinorVersion,
		"architecture", selector.Architecture,
		"variant", selector.Variant)

	// List all available images
	images, err := r.ListAvailableImages(ctx, "")
	if err != nil {
		r.logger.Error(err, "Failed to list available images")
		return "", fmt.Errorf("listing images: %w", err)
	}

	r.logger.Info("Retrieved images from VPC API", "totalImages", len(images))
	if len(images) > 0 && r.logger.V(1).Enabled() {
		// Log first few images for debugging
		for i, img := range images {
			if i >= 3 {
				break
			}
			r.logger.V(1).Info("Sample image", "index", i, "id", img.ID, "name", img.Name, "os", img.OperatingSystem, "status", img.Status)
		}
	}

	// Filter images by selector criteria
	candidates := r.filterImagesBySelector(images, selector)
	r.logger.Info("Filtered candidate images", "candidateCount", len(candidates))

	if len(candidates) > 0 && r.logger.V(1).Enabled() {
		r.logger.V(1).Info("First candidate image", "id", candidates[0].ID, "name", candidates[0].Name)
	}

	if len(candidates) == 0 {
		r.logger.Error(nil, "No images found matching selector criteria",
			"os", selector.OS,
			"majorVersion", selector.MajorVersion,
			"minorVersion", selector.MinorVersion,
			"architecture", selector.Architecture,
			"variant", selector.Variant,
			"totalImagesSearched", len(images))
		return "", fmt.Errorf("no images found matching selector: os=%s, majorVersion=%s, minorVersion=%s, architecture=%s, variant=%s",
			selector.OS, selector.MajorVersion, selector.MinorVersion, selector.Architecture, selector.Variant)
	}

	// Sort by semantic version and creation date (newest first)
	sortedCandidates := r.sortImagesByVersion(candidates, selector)

	r.logger.Info("Successfully resolved image using selector",
		"selectedImageID", sortedCandidates[0].ID,
		"selectedImageName", sortedCandidates[0].Name,
		"os", selector.OS,
		"majorVersion", selector.MajorVersion,
		"minorVersion", selector.MinorVersion,
		"architecture", selector.Architecture,
		"variant", selector.Variant)

	// Return the most recent matching image
	return sortedCandidates[0].ID, nil
}

// filterImagesBySelector filters images based on selector criteria
func (r *Resolver) filterImagesBySelector(images []ImageInfo, selector *v1alpha1.ImageSelector) []ImageInfo {
	var candidates []ImageInfo

	for _, img := range images {
		if r.imageMatchesSelector(img, selector) {
			candidates = append(candidates, img)
		}
	}

	return candidates
}

// imageMatchesSelector checks if an image matches the selector criteria
func (r *Resolver) imageMatchesSelector(img ImageInfo, selector *v1alpha1.ImageSelector) bool {
	// Parse image name to extract components
	// Supports multiple IBM Cloud image naming formats
	components := r.parseImageName(img.Name)
	if components == nil {
		return false
	}

	// Check OS match
	if components["os"] != selector.OS {
		return false
	}

	// Check major version match
	if components["majorVersion"] != selector.MajorVersion {
		return false
	}

	// Check minor version match (if specified)
	if selector.MinorVersion != "" {
		if components["minorVersion"] != selector.MinorVersion {
			return false
		}
	}

	// Check architecture match (default to amd64 if not specified)
	architecture := selector.Architecture
	if architecture == "" {
		architecture = "amd64"
	}
	if components["architecture"] != architecture {
		return false
	}

	// Check variant match (if specified)
	if selector.Variant != "" {
		if components["variant"] != selector.Variant {
			return false
		}
	}

	return true
}

// parseImageName extracts components from IBM Cloud image names
// Expected formats:
// - ibm-{os}-{major}-{minor}-{patch}-{variant}-{arch}-{build} (newer format)
// - ibm-{os}-{major}-{minor}-{variant}-{arch}-{build} (standard format)
// - ibm-{os}-{major}-{minor}-{arch}-{build} (alternative format)
// - {os}-{major}-{minor} (legacy format)
func (r *Resolver) parseImageName(imageName string) map[string]string {
	components := make(map[string]string)

	// Try newer IBM format first: ibm-{os}-{major}-{minor}-{patch}-{variant}-{arch}-{build}
	// Example: ibm-ubuntu-22-04-5-minimal-amd64-7
	ibmNewPattern := regexp.MustCompile(`^ibm-([a-z]+)-([0-9]+)-([0-9]+)-([0-9]+)-([a-z]+)-([a-z0-9]+)-([0-9]+)$`)
	matches := ibmNewPattern.FindStringSubmatch(imageName)
	if len(matches) == 8 {
		components["os"] = matches[1]
		components["majorVersion"] = matches[2]
		components["minorVersion"] = matches[3]
		components["patchVersion"] = matches[4]
		components["variant"] = matches[5]
		components["architecture"] = matches[6]
		components["build"] = matches[7]
		return components
	}

	// Try standard IBM format: ibm-{os}-{major}-{minor}-{variant}-{arch}-{build}
	// Example: ibm-ubuntu-22-04-minimal-amd64-1
	ibmPattern := regexp.MustCompile(`^ibm-([a-z]+)-([0-9]+)-([0-9]+)-([a-z]+)-([a-z0-9]+)-([0-9]+)$`)
	matches = ibmPattern.FindStringSubmatch(imageName)
	if len(matches) == 7 {
		components["os"] = matches[1]
		components["majorVersion"] = matches[2]
		components["minorVersion"] = matches[3]
		components["variant"] = matches[4]
		components["architecture"] = matches[5]
		components["build"] = matches[6]
		return components
	}

	// Try alternative IBM format: ibm-{os}-{major}-{minor}-{arch}-{build}
	ibmAltPattern := regexp.MustCompile(`^ibm-([a-z]+)-([0-9]+)-([0-9]+)-([a-z0-9]+)-([0-9]+)$`)
	matches = ibmAltPattern.FindStringSubmatch(imageName)
	if len(matches) == 6 {
		components["os"] = matches[1]
		components["majorVersion"] = matches[2]
		components["minorVersion"] = matches[3]
		components["variant"] = "minimal" // Default variant
		components["architecture"] = matches[4]
		components["build"] = matches[5]
		return components
	}

	// Try simple format: {os}-{major}-{minor}
	simplePattern := regexp.MustCompile(`^([a-z]+)-([0-9]+)-([0-9]+)$`)
	matches = simplePattern.FindStringSubmatch(imageName)
	if len(matches) == 4 {
		components["os"] = matches[1]
		components["majorVersion"] = matches[2]
		components["minorVersion"] = matches[3]
		components["variant"] = "minimal"    // Default variant
		components["architecture"] = "amd64" // Default architecture
		components["build"] = "1"            // Default build
		return components
	}

	return nil // Unable to parse
}

// sortImagesByVersion sorts images by semantic version and creation date
func (r *Resolver) sortImagesByVersion(images []ImageInfo, selector *v1alpha1.ImageSelector) []ImageInfo {
	sort.Slice(images, func(i, j int) bool {
		img1Components := r.parseImageName(images[i].Name)
		img2Components := r.parseImageName(images[j].Name)

		if img1Components == nil || img2Components == nil {
			// Fall back to creation date if parsing fails
			return images[i].CreatedAt.After(images[j].CreatedAt)
		}

		// If minor version not specified in selector, prefer higher minor versions
		if selector.MinorVersion == "" {
			minor1, _ := strconv.Atoi(img1Components["minorVersion"])
			minor2, _ := strconv.Atoi(img2Components["minorVersion"])
			if minor1 != minor2 {
				return minor1 > minor2
			}
		}

		// Prefer higher build numbers
		build1, _ := strconv.Atoi(img1Components["build"])
		build2, _ := strconv.Atoi(img2Components["build"])
		if build1 != build2 {
			return build1 > build2
		}

		// Finally, sort by creation date (newer first)
		return images[i].CreatedAt.After(images[j].CreatedAt)
	})

	return images
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
