# Load Balancer Integration

The Karpenter IBM Cloud Provider supports automatic registration and deregistration of nodes with IBM Cloud Load Balancers. This feature enables seamless integration with existing load balancing infrastructure without requiring external controllers or manual configuration.

## Overview

When load balancer integration is enabled, the Karpenter provider will:

1. **Automatically register** newly created nodes with specified load balancer pools
2. **Configure health checks** according to your specifications
3. **Automatically deregister** nodes when they are terminated or become unhealthy
4. **Validate configuration** to ensure load balancers and pools exist before node creation

## Configuration

Load balancer integration is configured in the `IBMNodeClass` specification under the `loadBalancerIntegration` field.

### Basic Configuration

```yaml
apiVersion: karpenter-ibm.sh/v1alpha1
kind: IBMNodeClass
metadata:
  name: lb-integrated-nodeclass
spec:
  # ... other nodeclass configuration ...

  loadBalancerIntegration:
    enabled: true
    targetGroups:
      - loadBalancerID: "r010-12345678-1234-5678-9abc-def012345678"
        poolName: "web-servers"
        port: 80
```

### Advanced Configuration

```yaml
loadBalancerIntegration:
  enabled: true
  autoDeregister: true
  registrationTimeout: 300

  targetGroups:
    # Multiple target groups supported
    - loadBalancerID: "r010-12345678-1234-5678-9abc-def012345678"
      poolName: "web-servers"
      port: 80
      weight: 50
      healthCheck:
        protocol: "http"
        path: "/health"
        interval: 30
        timeout: 5
        retryCount: 2

    - loadBalancerID: "r010-87654321-4321-8765-cba9-fedcba098765"
      poolName: "api-servers"
      port: 8080
      weight: 75
      healthCheck:
        protocol: "https"
        path: "/api/health"
        interval: 15
        timeout: 3
        retryCount: 3
```

## Configuration Reference

### LoadBalancerIntegration

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `enabled` | boolean | No | `false` | Enable/disable load balancer integration |
| `autoDeregister` | boolean | No | `true` | Automatically remove nodes from load balancers when terminated |
| `registrationTimeout` | int32 | No | `300` | Maximum time in seconds to wait for node registration |
| `targetGroups` | []LoadBalancerTarget | No | `[]` | List of load balancer target groups to register with |

### LoadBalancerTarget

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `loadBalancerID` | string | Yes | - | IBM Cloud Load Balancer ID (format: `r010-xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx`) |
| `poolName` | string | Yes | - | Name of the load balancer pool |
| `port` | int32 | Yes | - | Port number on target instances (1-65535) |
| `weight` | int32 | No | `50` | Load balancing weight (0-100) |
| `healthCheck` | LoadBalancerHealthCheck | No | - | Health check configuration |

### LoadBalancerHealthCheck

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `protocol` | string | No | `tcp` | Health check protocol (`tcp`, `http`, `https`) |
| `path` | string | No | - | URL path for HTTP/HTTPS health checks (required for HTTP/HTTPS) |
| `interval` | int32 | No | `30` | Health check interval in seconds (5-300) |
| `timeout` | int32 | No | `5` | Health check timeout in seconds (1-60) |
| `retryCount` | int32 | No | `2` | Number of consecutive successful checks required (1-10) |

## Examples

### Web Application with HTTP Health Checks

```yaml
loadBalancerIntegration:
  enabled: true
  targetGroups:
    - loadBalancerID: "r010-12345678-1234-5678-9abc-def012345678"
      poolName: "web-tier"
      port: 80
      healthCheck:
        protocol: "http"
        path: "/health"
        interval: 30
        timeout: 5
        retryCount: 2
```

### API Service with HTTPS Health Checks

```yaml
loadBalancerIntegration:
  enabled: true
  targetGroups:
    - loadBalancerID: "r010-87654321-4321-8765-cba9-fedcba098765"
      poolName: "api-tier"
      port: 8443
      weight: 75
      healthCheck:
        protocol: "https"
        path: "/api/v1/health"
        interval: 15
        timeout: 3
        retryCount: 3
```

### Multiple Load Balancers

```yaml
loadBalancerIntegration:
  enabled: true
  autoDeregister: true
  registrationTimeout: 300

  targetGroups:
    # Primary application load balancer
    - loadBalancerID: "r010-primary-lb-id"
      poolName: "app-servers"
      port: 8080
      weight: 50

    # Metrics/monitoring load balancer
    - loadBalancerID: "r010-monitoring-lb-id"
      poolName: "metrics-collection"
      port: 9090
      weight: 100
      healthCheck:
        protocol: "http"
        path: "/metrics"
        interval: 60
```

## Prerequisites

Before enabling load balancer integration, ensure:

1. **Load balancers exist**: The specified load balancer IDs must exist in your IBM Cloud account
2. **Pools are configured**: The named pools must exist within the load balancers
3. **Proper permissions**: The IBM Cloud API key used by Karpenter must have permissions to:
   - Read load balancer details
   - List and modify load balancer pool members
   - Read and write load balancer pool configurations

### Required IAM Permissions

The service ID or user used by Karpenter needs the following IAM permissions:

```json
{
  "roles": [
    {
      "role_id": "crn:v1:bluemix:public:iam::::role:Editor",
      "type": "service"
    }
  ],
  "resources": [
    {
      "attributes": [
        {
          "name": "serviceName",
          "value": "is"
        },
        {
          "name": "resourceType",
          "value": "load-balancer"
        }
      ]
    }
  ]
}
```
