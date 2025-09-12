# Security Considerations for Karpenter IBM Cloud Provider

## Overview

This document outlines important security considerations when using the Karpenter IBM Cloud Provider. Users should be aware of these potential security risks and take appropriate measures to mitigate them.

## Attack Surfaces

### 1. User Data and Bootstrap Scripts

The `IBMNodeClass` custom resource allows users to specify custom user data scripts that will be executed during node initialization. This is a powerful feature but also presents a significant security risk.

**Potential Risks:**
- **Script Injection:** Malicious scripts could be injected through user data
- **Privilege Escalation:** Scripts run with root privileges during node bootstrap

**Recommendations:**
- **Restrict Access:** Use Kubernetes RBAC to strictly limit who can create/modify `IBMNodeClass` resources
- **Code Review:** Always review user data scripts before deployment
- **Network Policies:** Implement network policies to restrict outbound connections during bootstrap

**Example RBAC Configuration:**
```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: ibmnodeclass-admin
rules:
- apiGroups: ["karpenter.ibm.sh"]
  resources: ["ibmnodeclasses"]
  verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: ibmnodeclass-admin-binding
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: ibmnodeclass-admin
subjects:
- kind: Group
  name: system:masters  # Restrict to cluster admins only
  apiGroup: rbac.authorization.k8s.io
```

### 2. API Credentials

The provider requires IBM Cloud API credentials to manage infrastructure resources.

**Potential Risks:**
- **Credential Exposure:** API keys in environment variables or configuration files
- **Insufficient Rotation:** Long-lived credentials increase risk window
- **Broad Permissions:** Over-privileged API keys

**Recommendations:**
- **Implement Rotation:** Regularly rotate API keys (recommended: every 90 days)
- **Principle of Least Privilege:** Create API keys with minimal required permissions
- **Audit Access:** Enable IBM Cloud audit logging for all API key usage

**Required IBM Cloud IAM Policies:**

The provider supports two deployment modes with different IAM requirements:

**For IKS (IBM Kubernetes Service) Deployments:**
```json
{
  "roles": [
    {
      "role": "Operator",
      "resources": [
        {
          "service": "containers-kubernetes",
          "resourceType": "cluster"
        }
      ]
    },
    {
      "role": "Viewer",
      "resources": [
        {
          "service": "is",
          "resourceType": "instance"
        }
      ]
    }
  ]
}
```

**For VPC (Self-Managed Kubernetes) Deployments:**
```json
{
  "roles": [
    {
      "role": "Editor",
      "resources": [
        {
          "service": "is",
          "resourceType": "instance"
        },
        {
          "service": "is",
          "resourceType": "subnet"
        },
        {
          "service": "is",
          "resourceType": "security-group"
        },
        {
          "service": "is",
          "resourceType": "image"
        },
        {
          "service": "is",
          "resourceType": "vpc"
        }
      ]
    },
    {
      "role": "Viewer",
      "resources": [
        {
          "service": "globalcatalog-collection"
        }
      ]
    }
  ]
}
```

**Deployment Mode Differences:**
- **IKS Mode**: Used with managed IBM Kubernetes Service clusters. Requires `Operator` role for worker pool resizing and worker node management, plus `Viewer` access to VPC instances for worker-to-instance mapping.
- **VPC Mode**: Used with self-managed Kubernetes clusters on IBM Cloud VPC. Requires full VPC resource management permissions for direct instance lifecycle management.
