# Getting Started with Karpenter IBM Cloud Provider

This guide walks you through setting up Karpenter IBM Cloud Provider from installation to your first auto-scaled workload.

## Prerequisites

Before starting, ensure you have:

### Required Access
- **IBM Cloud Account** with VPC Infrastructure Services access
- **Kubernetes Cluster** (IKS or self-managed on IBM Cloud VPC)
- **kubectl** configured for your cluster
- **Helm 3** for installation

### Required Tools
```bash
# Verify tools are available
kubectl version --client
helm version
ibmcloud version

# Install IBM Cloud CLI if needed
curl -fsSL https://clis.cloud.ibm.com/install/linux | sh
ibmcloud plugin install vpc-infrastructure
```

## IBM Cloud Setup

### Step 1: Create Service ID and API Keys
For production environments, use Service IDs for better security:

```bash
# Login to IBM Cloud
ibmcloud login

# Create Service ID for Karpenter
ibmcloud iam service-id-create karpenter-provider \
  --description "Service ID for Karpenter IBM Cloud Provider"

# Get Service ID
SERVICE_ID=$(ibmcloud iam service-ids --output json | jq -r '.[] | select(.name=="karpenter-provider") | .id')

# Assign VPC Infrastructure Services role
ibmcloud iam service-policy-create $SERVICE_ID \
  --roles "VPC Infrastructure Services" \
  --service-name is

# Create API keys
ibmcloud iam service-api-key-create karpenter-general $SERVICE_ID \
  --description "General IBM Cloud API access for Karpenter"

ibmcloud iam service-api-key-create karpenter-vpc $SERVICE_ID \
  --description "VPC-specific API access for Karpenter"
```

**Save the API keys securely - they won't be shown again!**

### Step 2: Gather Required Resource Information

```bash
# Set your target region
export REGION=us-south
ibmcloud target -r $REGION

# List available VPCs
ibmcloud is vpcs --output json

# Choose your VPC and list subnets
export VPC_ID="your-vpc-id"
ibmcloud is subnets --vpc $VPC_ID --output json

# List security groups in your VPC
ibmcloud is security-groups --vpc $VPC_ID --output json

# List available images
ibmcloud is images --visibility public --status available | grep ubuntu
```

**Collect the following information:**

- **VPC ID**: Must be an ID like `r006-4225852b-4846-4a4a-88c4-9966471337c6`
- **Subnet ID**: Format `02c7-718345b5-2de1-4a9a-b1de-fa7e307ee8c5`
- **Security Group ID**: Must be ID `r006-36f045e2-86a1-4af8-917e-b17a41f8abe3`
- **Image ID**: Prefer ID `r006-dd3c20fa-71d3-4dc0-913f-2f097bf3e500` (names like "ubuntu-22-04" work as well)
- **API Server Endpoint**: Get your cluster's API endpoint (e.g., `https://10.240.0.1:6443`)
- **Region**: `us-south` (or your preferred region)
- **Zone**: `us-south-1` (subnet's availability zone)
  - 💡 **Multi-Zone Tip**: For production deployments, consider using `placementStrategy` instead of explicit zones for automatic multi-zone distribution. See [Multi-Zone VPC Setup with Placement Constraints](vpc-integration.md#multi-zone-vpc-setup-with-placement-constraints)

## Installation

### Step 1: Install Helm Chart

Install directly with API keys as helm values:

```bash
# Add Helm repository
helm repo add karpenter-ibm https://karpenter-ibm.sh
helm repo update

# Install with your API keys
helm install karpenter karpenter-ibm/karpenter-ibm \
  --namespace karpenter \
  --create-namespace \
  --set credentials.region="us-south" \
  --set credentials.ibmApiKey="your-general-api-key" \
  --set credentials.vpcApiKey="your-vpc-api-key"
```

### Step 2: Verify Installation
```bash
# Check if pods are running
kubectl get pods -n karpenter

# Check controller logs for startup
kubectl logs -n karpenter deployment/karpenter -f

# Look for successful controller startup messages
# Expected: "Starting Controller" for nodepool, nodeclaim, nodeclass controllers
```

**Expected Output:**
```
Starting Controller {"controller": "nodepool", "controllerGroup": "karpenter.sh"}
Starting Controller {"controller": "nodeclaim", "controllerGroup": "karpenter.sh"}
Starting Controller {"controller": "nodeclass.hash", "controllerGroup": "karpenter.ibm.sh"}
Starting Controller {"controller": "pricing", "controllerGroup": "karpenter.ibm.sh"}
```

### Step 3: Create Your First NodeClass

Choose the configuration that matches your environment:

- **[IKS Integration](iks-integration.md)** - For IBM Kubernetes Service clusters
- **[VPC Integration](vpc-integration.md)** - For self-managed clusters on IBM Cloud VPC
